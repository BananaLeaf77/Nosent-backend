package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"log"

	"github.com/gofiber/fiber/v2"
	"github.com/yourorg/whatsapp-broadcast/internal/models"
	"github.com/yourorg/whatsapp-broadcast/internal/scheduler"
	"github.com/yourorg/whatsapp-broadcast/internal/whatsapp"
	"gorm.io/gorm"
)

type Handler struct {
	db    *gorm.DB
	wa    *whatsapp.Client
	sched *scheduler.Scheduler
}

func New(db *gorm.DB, wa *whatsapp.Client, sched *scheduler.Scheduler) *Handler {
	return &Handler{db: db, wa: wa, sched: sched}
}

func (h *Handler) RegisterRoutes(app *fiber.App) {
	api := app.Group("/api")

	// WhatsApp auth
	api.Get("/wa/status", h.WAStatus)
	api.Get("/wa/qr", h.WAQRCode)
	api.Post("/wa/logout", h.WALogout)
	api.Post("/wa/reconnect", h.WAReconnect)

	// Broadcasts
	api.Post("/broadcasts", h.CreateBroadcast)
	api.Get("/broadcasts", h.ListBroadcasts)
	api.Get("/broadcasts/:id", h.GetBroadcast)
	api.Delete("/broadcasts/:id", h.CancelBroadcast)
	api.Get("/broadcasts/:id/logs", h.GetLogs)
	api.Get("/broadcasts/:id/download", h.DownloadExcel)
}

// ─── WhatsApp ───────────────────────────────────────────────────────────────

func (h *Handler) WAStatus(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{
		"status": h.wa.GetStatus(),
	})
}

func (h *Handler) WAQRCode(c *fiber.Ctx) error {
	qr := h.wa.GetQRCode()
	if qr == "" {
		return c.JSON(fiber.Map{"qr": nil, "status": h.wa.GetStatus()})
	}
	return c.JSON(fiber.Map{"qr": qr, "status": h.wa.GetStatus()})
}

func (h *Handler) WAReconnect(c *fiber.Ctx) error {
	go func() {
		if h.wa.GetStatus() != whatsapp.StatusDisconnected {
			_ = h.wa.Logout()
		}
		if err := h.wa.Connect(); err != nil {
			log.Printf("[reconnect] Connect failed: %v", err)
		}
	}()
	return c.JSON(fiber.Map{"message": "reconnecting"})
}

func (h *Handler) WALogout(c *fiber.Ctx) error {
	if err := h.wa.Logout(); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	// Reconnect to show new QR
	go h.wa.Connect()
	return c.JSON(fiber.Map{"message": "logged out, reconnecting for new QR"})
}

// ─── Broadcasts ─────────────────────────────────────────────────────────────

type CreateBroadcastRequest struct {
	Name         string `form:"name"`
	MessageTpl   string `form:"message_tpl"`
	ScheduleType string `form:"schedule_type"` // "once" | "recurring"
	ScheduledAt  string `form:"scheduled_at"`  // ISO8601, for "once"
	CronExpr     string `form:"cron_expr"`     // for "recurring"
}

func (h *Handler) CreateBroadcast(c *fiber.Ctx) error {
	req := new(CreateBroadcastRequest)
	if err := c.BodyParser(req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid form data")
	}

	// Validate
	if req.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "name is required")
	}
	if req.MessageTpl == "" {
		return fiber.NewError(fiber.StatusBadRequest, "message_tpl is required")
	}

	// Handle file upload
	file, err := c.FormFile("excel")
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "excel file is required")
	}

	ext := filepath.Ext(file.Filename)
	if ext != ".xlsx" && ext != ".xls" {
		return fiber.NewError(fiber.StatusBadRequest, "only .xlsx or .xls files allowed")
	}

	// Save file
	uploadDir := "./uploads"
	os.MkdirAll(uploadDir, 0755)
	filename := fmt.Sprintf("%d_%s", time.Now().UnixMilli(), file.Filename)
	savePath := filepath.Join(uploadDir, filename)
	if err := c.SaveFile(file, savePath); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to save file")
	}

	// Build broadcast record
	broadcast := models.Broadcast{
		Name:         req.Name,
		ExcelPath:    savePath,
		ExcelName:    file.Filename,
		MessageTpl:   req.MessageTpl,
		ScheduleType: models.ScheduleType(req.ScheduleType),
		Status:       models.StatusPending,
	}

	switch req.ScheduleType {
	case string(models.ScheduleOnce):
		t, err := time.Parse(time.RFC3339, req.ScheduledAt)
		if err != nil {
			os.Remove(savePath)
			return fiber.NewError(fiber.StatusBadRequest, "invalid scheduled_at format, use ISO8601")
		}
		broadcast.ScheduledAt = &t

	case string(models.ScheduleRecurring):
		if req.CronExpr == "" {
			os.Remove(savePath)
			return fiber.NewError(fiber.StatusBadRequest, "cron_expr required for recurring schedule")
		}
		broadcast.CronExpr = req.CronExpr

	default:
		os.Remove(savePath)
		return fiber.NewError(fiber.StatusBadRequest, "schedule_type must be 'once' or 'recurring'")
	}

	// Save to DB first to get ID
	if err := h.db.Create(&broadcast).Error; err != nil {
		os.Remove(savePath)
		return fiber.NewError(fiber.StatusInternalServerError, "failed to save broadcast")
	}

	// Parse Excel and save patients
	patients, err := ParseExcel(savePath, broadcast.ID)
	if err != nil {
		h.db.Delete(&broadcast)
		os.Remove(savePath)
		return fiber.NewError(fiber.StatusBadRequest, "excel parse error: "+err.Error())
	}
	if len(patients) == 0 {
		h.db.Delete(&broadcast)
		os.Remove(savePath)
		return fiber.NewError(fiber.StatusBadRequest, "no valid patients found in Excel")
	}

	if err := h.db.Create(&patients).Error; err != nil {
		h.db.Delete(&broadcast)
		os.Remove(savePath)
		return fiber.NewError(fiber.StatusInternalServerError, "failed to save patients")
	}

	h.db.Model(&broadcast).Update("total_count", len(patients))

	// Register with scheduler
	switch broadcast.ScheduleType {
	case models.ScheduleOnce:
		if err := h.sched.ScheduleOnce(&broadcast); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
	case models.ScheduleRecurring:
		if err := h.sched.ScheduleRecurring(&broadcast); err != nil {
			return fiber.NewError(fiber.StatusBadRequest, err.Error())
		}
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id":            broadcast.ID,
		"name":          broadcast.Name,
		"excel_name":    broadcast.ExcelName,
		"schedule_type": broadcast.ScheduleType,
		"scheduled_at":  broadcast.ScheduledAt,
		"cron_expr":     broadcast.CronExpr,
		"status":        broadcast.Status,
		"total_count":   broadcast.TotalCount,
		"sent_count":    broadcast.SentCount,
		"failed_count":  broadcast.FailedCount,
		"created_at":    broadcast.CreatedAt,
	})
}

func (h *Handler) ListBroadcasts(c *fiber.Ctx) error {
	var results []models.BroadcastSummary
	h.db.Model(&models.Broadcast{}).
		Select("id, name, excel_name, schedule_type, scheduled_at, cron_expr, status, total_count, sent_count, failed_count, last_sent_at, created_at").
		Order("created_at DESC").
		Scan(&results)

	return c.JSON(results)
}

func (h *Handler) GetBroadcast(c *fiber.Ctx) error {
	id := c.Params("id")
	var b models.Broadcast
	if err := h.db.Preload("Patients").First(&b, id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "broadcast not found")
	}
	return c.JSON(b)
}

func (h *Handler) CancelBroadcast(c *fiber.Ctx) error {
	id, err := c.ParamsInt("id")
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	var b models.Broadcast
	if err := h.db.First(&b, id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "broadcast not found")
	}

	h.sched.Cancel(uint(id))
	h.db.Model(&b).Update("status", models.StatusCancelled)

	return c.JSON(fiber.Map{"message": "broadcast cancelled"})
}

func (h *Handler) GetLogs(c *fiber.Ctx) error {
	id := c.Params("id")
	var logs []models.MessageLog
	h.db.Where("broadcast_id = ?", id).Order("created_at DESC").Find(&logs)
	return c.JSON(logs)
}

func (h *Handler) DownloadExcel(c *fiber.Ctx) error {
	id := c.Params("id")
	var b models.Broadcast
	if err := h.db.First(&b, id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "broadcast not found")
	}

	if _, err := os.Stat(b.ExcelPath); os.IsNotExist(err) {
		return fiber.NewError(fiber.StatusNotFound, "file not found on disk")
	}

	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, b.ExcelName))
	return c.SendFile(b.ExcelPath)
}