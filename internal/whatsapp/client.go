package whatsapp

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

type Status string

const (
	StatusDisconnected Status = "disconnected"
	StatusWaitingQR    Status = "waiting_qr"
	StatusConnected    Status = "connected"

	// qrTimeout is how long we wait for the user to scan before giving up.
	// WhatsApp issues a new QR code every ~20s; we allow up to 10 rounds (≈3 min).
	qrTimeout = 3 * time.Minute
)

type Client struct {
	mu       sync.RWMutex
	waClient *whatsmeow.Client
	db       *gorm.DB
	status   Status
	qrChan   chan string
	qrCode   string // latest QR as raw string
}

func NewClient(db *gorm.DB) *Client {
	return &Client{
		db:     db,
		status: StatusDisconnected,
		qrChan: make(chan string, 1),
	}
}

// Connect initialises the whatsmeow client and restores session if available.
func (c *Client) Connect() error {
	ctx := context.Background()
	dbAddress := getEnv("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/nosent?sslmode=disable")
	dbLog := waLog.Stdout("Database", "WARN", true)

	container, err := sqlstore.New(ctx, "postgres", dbAddress, dbLog)
	if err != nil {
		return fmt.Errorf("sqlstore: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		return fmt.Errorf("get device: %w", err)
	}

	clientLog := waLog.Stdout("Client", "WARN", true)
	c.waClient = whatsmeow.NewClient(deviceStore, clientLog)
	c.waClient.AddEventHandler(c.handleEvent)

	if c.waClient.Store.ID == nil {
		// New device — need QR scan.
		// Use a long-lived context so the QR channel stays open for qrTimeout.
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
					// New QR code issued — store it so the frontend can poll it.
					c.mu.Lock()
					c.qrCode = evt.Code
					c.mu.Unlock()
					select {
					case c.qrChan <- evt.Code:
					default:
					}
				case "success":
					// Scanned successfully; handleEvent will set StatusConnected.
				case "timeout":
					// WhatsApp stopped issuing QR codes — stay in waiting_qr so
					// the user can trigger a reconnect from the UI.
					c.setStatus(StatusDisconnected)
				}
			}
		}()
	} else {
		// Existing session — restore it directly.
		if err := c.waClient.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		c.setStatus(StatusConnected)
	}

	return nil
}

// GetQRCode returns the current QR code string (for frontend polling).
func (c *Client) GetQRCode() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.qrCode
}

// GetStatus returns current connection status.
func (c *Client) GetStatus() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.status
}

// SendMessage sends a text message to a phone number like "6281234567890".
func (c *Client) SendMessage(phone, message string) error {
	if c.GetStatus() != StatusConnected {
		return fmt.Errorf("whatsapp not connected")
	}

	jid, err := types.ParseJID(phone + "@s.whatsapp.net")
	if err != nil {
		return fmt.Errorf("invalid phone %s: %w", phone, err)
	}

	msg := &waProto.Message{
		Conversation: proto.String(message),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err = c.waClient.SendMessage(ctx, jid, msg)
	return err
}

// Logout disconnects and clears the session.
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
