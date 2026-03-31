package whatsapp

import (
	"log"
	"sync"

	"github.com/Nosent/whatsapp-broadcast/internal/models"
	"gorm.io/gorm"
)

// Manager holds one whatsmeow Client per app user.
// It enables "1 app user -> 1 WhatsApp session" (QR + device store).
type Manager struct {
	db *gorm.DB

	mu sync.Mutex
	// username -> whatsapp client
	clients map[string]*Client

	defaultUsername string
}

func NewManager(db *gorm.DB) *Manager {
	m := &Manager{
		db:      db,
		clients: make(map[string]*Client),
	}

	// Used as a fallback for older broadcasts that don't have an owner yet.
	var admins []models.Admin
	if err := db.Order("created_at ASC").Limit(1).Find(&admins).Error; err == nil && len(admins) > 0 {
		m.defaultUsername = admins[0].Username
	}

	return m
}

func (m *Manager) GetClient(username string) *Client {
	if username == "" {
		username = m.defaultUsername
	}
	if username == "" {
		// No admin in DB (or empty jwt); caller will get a client but it won't connect.
		log.Printf("[whatsapp manager] empty username and no fallback admin found")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if c := m.clients[username]; c != nil {
		return c
	}

	c := NewClient(m.db, username)
	m.clients[username] = c
	return c
}

