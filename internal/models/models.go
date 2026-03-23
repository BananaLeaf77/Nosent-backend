package models

import (
	"time"

	"gorm.io/gorm"
)

// BroadcastStatus represents the state of a broadcast
type BroadcastStatus string

const (
	StatusPending   BroadcastStatus = "pending"
	StatusSending   BroadcastStatus = "sending"
	StatusCompleted BroadcastStatus = "completed"
	StatusFailed    BroadcastStatus = "failed"
	StatusCancelled BroadcastStatus = "cancelled"
)

// ScheduleType defines how the broadcast recurs
type ScheduleType string

const (
	ScheduleOnce      ScheduleType = "once"
	ScheduleRecurring ScheduleType = "recurring"
)

// Broadcast represents one scheduled broadcast session
type Broadcast struct {
	gorm.Model
	Name         string          `json:"name" gorm:"not null"`
	ExcelPath    string          `json:"excel_path" gorm:"not null"`
	ExcelName    string          `json:"excel_name" gorm:"not null"`
	MessageTpl   string          `json:"message_tpl" gorm:"not null"`
	ScheduleType ScheduleType    `json:"schedule_type" gorm:"not null"`
	ScheduledAt  *time.Time      `json:"scheduled_at"`
	CronExpr     string          `json:"cron_expr"`
	Status       BroadcastStatus `json:"status" gorm:"default:'pending'"`
	TotalCount   int             `json:"total_count"`
	SentCount    int             `json:"sent_count"`
	FailedCount  int             `json:"failed_count"`
	LastSentAt   *time.Time      `json:"last_sent_at"`
	CronID       int             `json:"cron_id" gorm:"-"`
	Patients     []Patient       `json:"patients,omitempty" gorm:"foreignKey:BroadcastID"`
	Logs         []MessageLog    `json:"logs,omitempty" gorm:"foreignKey:BroadcastID"`
}

// Patient is one row from the uploaded Excel.
// Columns match the clinic's format:
//   Nama Pasien | No Telp | Alamat | HPHT | Hamil Ke-
type Patient struct {
	gorm.Model
	BroadcastID     uint   `json:"broadcast_id" gorm:"not null;index"`
	Name            string `json:"name" gorm:"not null"`            // Nama Pasien
	Phone           string `json:"phone" gorm:"not null"`           // No Telp
	Address         string `json:"address"`                         // Alamat
	HPHT            string `json:"hpht"`                            // Hari Pertama Haid Terakhir
	PregnancyNumber string `json:"pregnancy_number"`                // Hamil Ke-
}

// MessageLog tracks each individual send attempt
type MessageLog struct {
	gorm.Model
	BroadcastID uint      `json:"broadcast_id" gorm:"not null;index"`
	PatientID   uint      `json:"patient_id" gorm:"not null;index"`
	PatientName string    `json:"patient_name"`
	Phone       string    `json:"phone"`
	Status      string    `json:"status"` // "sent" | "failed"
	Error       string    `json:"error"`
	SentAt      time.Time `json:"sent_at"`
}

// BroadcastSummary is a lightweight read model for the history list
type BroadcastSummary struct {
	ID           uint            `json:"id"`
	Name         string          `json:"name"`
	ExcelName    string          `json:"excel_name"`
	ScheduleType ScheduleType    `json:"schedule_type"`
	ScheduledAt  *time.Time      `json:"scheduled_at"`
	CronExpr     string          `json:"cron_expr"`
	Status       BroadcastStatus `json:"status"`
	TotalCount   int             `json:"total_count"`
	SentCount    int             `json:"sent_count"`
	FailedCount  int             `json:"failed_count"`
	LastSentAt   *time.Time      `json:"last_sent_at"`
	CreatedAt    time.Time       `json:"created_at"`
}

// Admin represents an authenticated user in the system
type Admin struct {
	gorm.Model
	Username string `gorm:"uniqueIndex;not null"`
	Password string `gorm:"not null"` // Hashed password
}