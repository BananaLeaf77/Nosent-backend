package handlers

import (
	"os"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/Nosent/whatsapp-broadcast/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

type AuthHandler struct {
	db *gorm.DB
}

func NewAuthHandler(db *gorm.DB) *AuthHandler {
	return &AuthHandler{db: db}
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Login validates credentials and returns a signed JWT.
func (h *AuthHandler) Login(c *fiber.Ctx) error {
	var req loginRequest
	if err := c.BodyParser(&req); err != nil || req.Username == "" || req.Password == "" {
		return fiber.NewError(fiber.StatusBadRequest, "username and password required")
	}

	var admin models.Admin
	if err := h.db.Where("username = ?", req.Username).First(&admin).Error; err != nil {
		// Constant-time failure (don't reveal whether user exists)
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$dummyhashtopreventtimingattacks000000000000000000000000"), []byte(req.Password))
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(admin.Password), []byte(req.Password)); err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "invalid credentials")
	}

	token, err := issueToken(admin.Username)
	if err != nil {
		return fiber.NewError(fiber.StatusInternalServerError, "could not generate token")
	}

	return c.JSON(fiber.Map{"token": token})
}

// issueToken creates a signed JWT valid for 30 days.
func issueToken(username string) (string, error) {
	claims := jwt.MapClaims{
		"sub": username,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(jwtSecret()))
}

// jwtSecret is the single source of truth for the signing key in this package.
func jwtSecret() string {
	if s := os.Getenv("JWT_SECRET"); s != "" {
		return s
	}
	return "changeme-please-set-JWT_SECRET-in-env"
}
