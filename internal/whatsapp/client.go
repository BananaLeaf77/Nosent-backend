package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	"github.com/Nosent/whatsapp-broadcast/internal/models"
	"gorm.io/gorm"
)

type Status string

const (
	StatusDisconnected Status = "disconnected"
	StatusWaitingQR    Status = "waiting_qr"
	StatusConnected    Status = "connected"

	qrTimeout = 3 * time.Minute
)

type Client struct {
	mu          sync.RWMutex
	connectMu  sync.Mutex
	waClient    *whatsmeow.Client
	db          *gorm.DB
	username    string
	status      Status
	qrChan      chan string
	qrCode      string
}

func NewClient(db *gorm.DB, username string) *Client {
	return &Client{
		db:       db,
		username: username,
		status:   StatusDisconnected,
		qrChan:   make(chan string, 1),
	}
}

func (c *Client) Connect() error {
	ctx := context.Background()
	dbAddress := getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/nosent?sslmode=disable")
	dbLog := waLog.Stdout("Database", "WARN", true)

	container, err := sqlstore.New(ctx, "postgres", dbAddress, dbLog)
	if err != nil {
		return fmt.Errorf("sqlstore: %w", err)
	}

	deviceStore := (*store.Device)(nil)
	// Load existing device for this app user (1 user -> 1 WhatsApp session).
	if c.username != "" {
		var sess models.WhatsAppSession
		if err := c.db.Where("username = ?", c.username).First(&sess).Error; err == nil && sess.JID != "" {
			jid, parseErr := types.ParseJID(sess.JID)
			if parseErr == nil {
				ds, getErr := container.GetDevice(ctx, jid)
				if getErr != nil {
					return fmt.Errorf("get device for %s: %w", c.username, getErr)
				}
				deviceStore = ds
			}
		}
		// If no session exists (or parsing failed), fall back to a new device.
	}
	if deviceStore == nil {
		deviceStore = container.NewDevice()
	}

	clientLog := waLog.Stdout("Client", "WARN", true)
	c.waClient = whatsmeow.NewClient(deviceStore, clientLog)
	c.waClient.AddEventHandler(c.handleEvent)

	if c.waClient.Store.ID == nil {
		qrCtx, qrCancel := context.WithTimeout(ctx, qrTimeout)

		qrChan, _ := c.waClient.GetQRChannel(qrCtx)
		if err := c.waClient.Connect(); err != nil {
			qrCancel()
			return fmt.Errorf("connect: %w", err)
		}
		c.setStatus(StatusWaitingQR)

		go func() {
			defer qrCancel()
			for evt := range qrChan {
				switch evt.Event {
				case "code":
					c.mu.Lock()
					c.qrCode = evt.Code
					c.mu.Unlock()
					select {
					case c.qrChan <- evt.Code:
					default:
					}
				case "success":
					// handleEvent will set StatusConnected
					// and persist this user's WhatsApp session JID.
				case "timeout":
					c.setStatus(StatusDisconnected)
				}
			}
		}()
	} else {
		if err := c.waClient.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		c.setStatus(StatusConnected)
		c.persistSession()
	}

	return nil
}

func (c *Client) EnsureConnected() error {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	// If we're already connected (or waiting QR), don't spam connect.
	if c.GetStatus() == StatusConnected || c.GetStatus() == StatusWaitingQR {
		return nil
	}
	return c.Connect()
}

func (c *Client) GetQRCode() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.qrCode
}

func (c *Client) GetStatus() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

func (c *Client) persistSession() {
	if c.username == "" || c.waClient == nil || c.waClient.Store.ID == nil {
		return
	}

	jidStr := c.waClient.Store.ID.String()
	var sess models.WhatsAppSession
	err := c.db.Where("username = ?", c.username).First(&sess).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			_ = c.db.Create(&models.WhatsAppSession{
				Username: c.username,
				JID:       jidStr,
			}).Error
			return
		}
		// Ignore persistence errors to avoid breaking message sending.
		return
	}

	// Upsert
	if sess.JID != jidStr {
		_ = c.db.Model(&sess).Update("jid", jidStr).Error
	}
}

// SendMessage sends a reliable WhatsApp message to a number like "6281234567890".
// Uses ExtendedTextMessage and forces device synchronization to prevent iOS single-tick issues.
func (c *Client) SendMessage(phone, message string) error {
	if c.GetStatus() != StatusConnected {
		return fmt.Errorf("whatsapp not connected")
	}

	jid, err := types.ParseJID(phone + "@s.whatsapp.net")
	if err != nil {
		return fmt.Errorf("invalid phone %s: %w", phone, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Force device synchronization to ensure encryption keys are updated
	// This helps mitigate the single-checkmark issue observed on iOS devices.
	_ = c.waClient.SendPresence(ctx, types.PresenceAvailable)
	// We also subscribe to their presence to ensure the device wakes up to receive our message.
	_ = c.waClient.SubscribePresence(ctx, jid)

	// Use ExtendedTextMessage instead of plain Conversation.
	// Modern iOS devices process this more reliably than raw text.
	msg := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: proto.String(message),
		},
	}

	_, err = c.waClient.SendMessage(ctx, jid, msg)
	return err
}

func (c *Client) Logout() error {
	if c.waClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	err := c.waClient.Logout(ctx)
	c.setStatus(StatusDisconnected)
	c.mu.Lock()
	c.qrCode = ""
	c.mu.Unlock()
	return err
}

func (c *Client) handleEvent(evt interface{}) {
	switch evt.(type) {
	case *events.Connected:
		c.setStatus(StatusConnected)
		c.persistSession()
	case *events.Disconnected:
		c.setStatus(StatusDisconnected)
	case *events.LoggedOut:
		c.setStatus(StatusDisconnected)
		c.mu.Lock()
		c.qrCode = ""
		c.mu.Unlock()
	}
}

func (c *Client) setStatus(s Status) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = s
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}