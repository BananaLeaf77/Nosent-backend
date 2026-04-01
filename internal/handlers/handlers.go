package handlers

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"log"

	"github.com/Nosent/whatsapp-broadcast/internal/middleware"
	"github.com/Nosent/whatsapp-broadcast/internal/models"
	"github.com/Nosent/whatsapp-broadcast/internal/scheduler"
	"github.com/Nosent/whatsapp-broadcast/internal/whatsapp"
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

type Handler struct {
	db    *gorm.DB
	wa    *whatsapp.Manager
	sched *scheduler.Scheduler
}

func New(db *gorm.DB, wa *whatsapp.Manager, sched *scheduler.Scheduler) *Handler {
	return &Handler{db: db, wa: wa, sched: sched}
}

func (h *Handler) RegisterRoutes(app *fiber.App) {
	api := app.Group("/api")

	// ── Public ──────────────────────────────────────────────────────
	auth := NewAuthHandler(h.db)
	api.Post("/auth/login", auth.Login)

	// ── Protected (JWT required) ─────────────────────────────────────
	protected := api.Group("", middleware.Auth())

	// WhatsApp
	protected.Get("/wa/status", h.WAStatus)
	protected.Get("/wa/qr", h.WAQRCode)
	protected.Get("/wa/me", h.WAMe)
	protected.Post("/wa/logout", h.WALogout)
	protected.Post("/wa/reconnect", h.WAReconnect)

	// Broadcasts
	protected.Post("/broadcasts", h.CreateBroadcast)
	protected.Get("/broadcasts", h.ListBroadcasts)
	protected.Get("/broadcasts/:id", h.GetBroadcast)
	protected.Delete("/broadcasts/:id", h.CancelBroadcast)
	protected.Get("/broadcasts/:id/logs", h.GetLogs)
	protected.Get("/broadcasts/:id/download", h.DownloadExcel)
}

// ─── WhatsApp ───────────────────────────────────────────────────────────────

func (h *Handler) WAStatus(c *fiber.Ctx) error {
	user, _ := c.Locals("user").(string)
	if user == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "missing user")
	}
	waClient := h.wa.GetClient(user)
	_ = waClient.EnsureConnected()
	return c.JSON(fiber.Map{
		"status": waClient.GetStatus(),
	})
}

func (h *Handler) WAQRCode(c *fiber.Ctx) error {
	user, _ := c.Locals("user").(string)
	if user == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "missing user")
	}

	waClient := h.wa.GetClient(user)
	_ = waClient.EnsureConnected()

	qr := waClient.GetQRCode()
	if qr == "" {
		return c.JSON(fiber.Map{"qr": nil, "status": waClient.GetStatus()})
	}
	return c.JSON(fiber.Map{"qr": qr, "status": waClient.GetStatus()})
}

// WAMe returns the connected WhatsApp phone number (JID) for the logged-in user.
func (h *Handler) WAMe(c *fiber.Ctx) error {
	user, _ := c.Locals("user").(string)
	if user == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "missing user")
	}

	waClient := h.wa.GetClient(user)
	phone := waClient.GetPhone()

	return c.JSON(fiber.Map{
		"phone":    phone, // e.g. "6281234567890" or "" if not connected
		"username": user,
	})
}

func (h *Handler) WAReconnect(c *fiber.Ctx) error {
	user, _ := c.Locals("user").(string)
	if user == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "missing user")
	}

	waClient := h.wa.GetClient(user)
	go func() {
		if waClient.GetStatus() != whatsapp.StatusDisconnected {
			_ = waClient.Logout()
		}
		if err := waClient.Connect(); err != nil {
			log.Printf("[reconnect] Connect failed: %v", err)
		}
	}()
	return c.JSON(fiber.Map{"message": "reconnecting"})
}

func (h *Handler) WALogout(c *fiber.Ctx) error {
	user, _ := c.Locals("user").(string)
	if user == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "missing user")
	}
	waClient := h.wa.GetClient(user)

	if err := waClient.Logout(); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, err.Error())
	}
	// Reconnect to show new QR
	go waClient.Connect()
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
	user, _ := c.Locals("user").(string)
	if user == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "missing user")
	}

	req := new(CreateBroadcastRequest)
	if err := c.BodyParser(req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid form data")
	}

	if req.Name == "" {
		return fiber.NewError(fiber.StatusBadRequest, "name is required")
	}
	if req.MessageTpl == "" {
		return fiber.NewError(fiber.StatusBadRequest, "message_tpl is required")
	}

	file, err := c.FormFile("excel")
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "excel file is required")
	}

	ext := filepath.Ext(file.Filename)
	if ext != ".xlsx" && ext != ".xls" {
		return fiber.NewError(fiber.StatusBadRequest, "only .xlsx or .xls files allowed")
	}

	uploadDir := "./uploads"
	os.MkdirAll(uploadDir, 0755)
	filename := fmt.Sprintf("%d_%s", time.Now().UnixMilli(), file.Filename)
	savePath := filepath.Join(uploadDir, filename)
	if err := c.SaveFile(file, savePath); err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "failed to save file")
	}

	broadcast := models.Broadcast{
		Name:          req.Name,
		ExcelPath:     savePath,
		ExcelName:     file.Filename,
		MessageTpl:    req.MessageTpl,
		ScheduleType:  models.ScheduleType(req.ScheduleType),
		OwnerUsername: user,
		Status:        models.StatusPending,
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

	if err := h.db.Create(&broadcast).Error; err != nil {
		os.Remove(savePath)
		return fiber.NewError(fiber.StatusInternalServerError, "failed to save broadcast")
	}

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
	user, _ := c.Locals("user").(string)
	if user == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "missing user")
	}

	var results []models.BroadcastSummary
	h.db.Model(&models.Broadcast{}).
		Where("owner_username = ?", user).
		Select("id, name, excel_name, schedule_type, scheduled_at, cron_expr, status, total_count, sent_count, failed_count, last_sent_at, created_at").
		Order("created_at DESC").
		Scan(&results)

	return c.JSON(results)
}

func (h *Handler) GetBroadcast(c *fiber.Ctx) error {
	user, _ := c.Locals("user").(string)
	if user == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "missing user")
	}
	id := c.Params("id")
	var b models.Broadcast
	if err := h.db.Preload("Patients").Where("id = ? AND owner_username = ?", id, user).First(&b).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "broadcast not found")
	}
	return c.JSON(b)
}

func (h *Handler) CancelBroadcast(c *fiber.Ctx) error {
	user, _ := c.Locals("user").(string)
	if user == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "missing user")
	}
	id, err := c.ParamsInt("id")
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "invalid id")
	}

	var b models.Broadcast
	if err := h.db.Where("id = ? AND owner_username = ?", id, user).First(&b).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "broadcast not found")
	}

	h.sched.Cancel(uint(id))
	h.db.Model(&b).Update("status", models.StatusCancelled)

	return c.JSON(fiber.Map{"message": "broadcast cancelled"})
}

func (h *Handler) GetLogs(c *fiber.Ctx) error {
	user, _ := c.Locals("user").(string)
	if user == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "missing user")
	}
	id := c.Params("id")
	var logs []models.MessageLog
	h.db.Model(&models.MessageLog{}).
		Joins("JOIN broadcasts ON broadcasts.id = message_logs.broadcast_id").
		Where("broadcasts.id = ? AND broadcasts.owner_username = ?", id, user).
		Order("message_logs.created_at DESC").
		Find(&logs)
	return c.JSON(logs)
}

func (h *Handler) DownloadExcel(c *fiber.Ctx) error {
	user, _ := c.Locals("user").(string)
	if user == "" {
		return fiber.NewError(fiber.StatusUnauthorized, "missing user")
	}
	id := c.Params("id")
	var b models.Broadcast
	if err := h.db.Where("id = ? AND owner_username = ?", id, user).First(&b).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "broadcast not found")
	}

	if _, err := os.Stat(b.ExcelPath); os.IsNotExist(err) {
		return fiber.NewError(fiber.StatusNotFound, "file not found on disk")
	}

	c.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, b.ExcelName))
	return c.SendFile(b.ExcelPath)
}
