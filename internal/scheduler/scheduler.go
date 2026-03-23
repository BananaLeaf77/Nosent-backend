package scheduler

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/yourorg/whatsapp-broadcast/internal/models"
	"github.com/yourorg/whatsapp-broadcast/internal/whatsapp"
	"gorm.io/gorm"
)

type Scheduler struct {
	mu      sync.Mutex
	cron    *cron.Cron
	db      *gorm.DB
	wa      *whatsapp.Client
	entries map[uint]cron.EntryID // broadcastID -> cron entry
}

func New(db *gorm.DB, wa *whatsapp.Client) *Scheduler {
	return &Scheduler{
		db:      db,
		wa:      wa,
		cron:    cron.New(cron.WithSeconds()),
		entries: make(map[uint]cron.EntryID),
	}
}

func (s *Scheduler) Start() {
	s.cron.Start()
	s.restorePending()
	s.cron.AddFunc("0 0 3 * * *", s.cleanOldFiles)
}

func (s *Scheduler) Stop() {
	s.cron.Stop()
}

// ScheduleOnce schedules a one-time broadcast at a specific time.
func (s *Scheduler) ScheduleOnce(b *models.Broadcast) error {
	if b.ScheduledAt == nil {
		return fmt.Errorf("scheduled_at is nil")
	}
	delay := time.Until(*b.ScheduledAt)
	if delay < 0 {
		return fmt.Errorf("scheduled time is in the past")
	}

	go func() {
		time.Sleep(delay)
		s.executeBroadcast(b.ID)
	}()

	return nil
}

// ScheduleRecurring registers a cron expression for a broadcast.
func (s *Scheduler) ScheduleRecurring(b *models.Broadcast) error {
	entryID, err := s.cron.AddFunc(b.CronExpr, func() {
		s.executeBroadcast(b.ID)
	})
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", b.CronExpr, err)
	}

	s.mu.Lock()
	s.entries[b.ID] = entryID
	s.mu.Unlock()
	return nil
}

// Cancel removes a recurring schedule.
func (s *Scheduler) Cancel(broadcastID uint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entryID, ok := s.entries[broadcastID]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, broadcastID)
	}
}

func (s *Scheduler) cleanOldFiles() {
	cutoff := time.Now().AddDate(0, 0, -30)
	var old []models.Broadcast
	s.db.Where("created_at < ? AND excel_path != ''", cutoff).Find(&old)
	for _, b := range old {
		os.Remove(b.ExcelPath)
		s.db.Model(&b).Update("excel_path", "")
		log.Printf("[cleanup] deleted excel for broadcast %d", b.ID)
	}
}

// executeBroadcast fetches patients and sends messages.
func (s *Scheduler) executeBroadcast(broadcastID uint) {
	var b models.Broadcast
	if err := s.db.First(&b, broadcastID).Error; err != nil {
		log.Printf("[scheduler] broadcast %d not found: %v", broadcastID, err)
		return
	}
	if b.Status == models.StatusCancelled {
		return
	}

	var patients []models.Patient
	if err := s.db.Where("broadcast_id = ?", broadcastID).Find(&patients).Error; err != nil {
		log.Printf("[scheduler] load patients failed: %v", err)
		return
	}

	now := time.Now()
	s.db.Model(&b).Updates(map[string]interface{}{
		"status":      models.StatusSending,
		"total_count": len(patients),
	})

	sentCount, failedCount := 0, 0

	for _, p := range patients {
		msg := buildMessage(b.MessageTpl, &p)
		err := s.wa.SendMessage(p.Phone, msg)

		logEntry := models.MessageLog{
			BroadcastID: broadcastID,
			PatientID:   p.ID,
			PatientName: p.Name,
			Phone:       p.Phone,
			SentAt:      time.Now(),
		}

		if err != nil {
			logEntry.Status = "failed"
			logEntry.Error = err.Error()
			failedCount++
			log.Printf("[scheduler] send to %s failed: %v", p.Phone, err)
		} else {
			logEntry.Status = "sent"
			sentCount++
		}

		s.db.Create(&logEntry)

		// Polite delay between messages to avoid rate limiting
		time.Sleep(1500 * time.Millisecond)
	}

	finalStatus := models.StatusCompleted
	if failedCount > 0 && sentCount == 0 {
		finalStatus = models.StatusFailed
	}

	s.db.Model(&b).Updates(map[string]interface{}{
		"status":       finalStatus,
		"sent_count":   sentCount,
		"failed_count": failedCount,
		"last_sent_at": now,
	})

	log.Printf("[scheduler] broadcast %d done: %d sent, %d failed", broadcastID, sentCount, failedCount)
}

// buildMessage replaces template placeholders with patient data.
// Available placeholders:
//
//	{{name}}             — Nama Pasien
//	{{phone}}            — No Telp
//	{{address}}          — Alamat
//	{{hpht}}             — HPHT (tgl mens terakhir)
//	{{pregnancy_number}} — Hamil Ke-
func buildMessage(tpl string, p *models.Patient) string {
	r := strings.NewReplacer(
		"{{name}}", p.Name,
		"{{phone}}", p.Phone,
		"{{address}}", p.Address,
		"{{hpht}}", p.HPHT,
		"{{pregnancy_number}}", p.PregnancyNumber,
	)
	return r.Replace(tpl)
}

// restorePending re-registers any pending recurring broadcasts after restart.
func (s *Scheduler) restorePending() {
	var broadcasts []models.Broadcast
	s.db.Where("schedule_type = ? AND status NOT IN ?", models.ScheduleRecurring,
		[]models.BroadcastStatus{models.StatusCancelled}).
		Find(&broadcasts)

	for i := range broadcasts {
		b := &broadcasts[i]
		if err := s.ScheduleRecurring(b); err != nil {
			log.Printf("[scheduler] restore broadcast %d failed: %v", b.ID, err)
		} else {
			log.Printf("[scheduler] restored recurring broadcast %d (%s)", b.ID, b.CronExpr)
		}
	}
}
