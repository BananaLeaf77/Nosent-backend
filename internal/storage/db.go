package storage

import (
	"fmt"
	"log"
	"os"

	"github.com/Nosent/whatsapp-broadcast/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Connect() (*gorm.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		// Build from parts (local dev)
		dsn = fmt.Sprintf(
			"host=%s user=%s password=%s dbname=%s port=%s sslmode=disable",
			getEnv("DB_HOST", "localhost"),
			getEnv("DB_USER", "postgres"),
			getEnv("DB_PASSWORD", "postgres"),
			getEnv("DB_NAME", "whatsapp_broadcast"),
			getEnv("DB_PORT", "5432"),
		)
	}

	logLevel := logger.Silent
	if os.Getenv("ENV") == "development" {
		logLevel = logger.Info
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logLevel),
	})
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(10)
	sqlDB.SetMaxIdleConns(5)

	// Create admin
	if err := db.AutoMigrate(&models.Admin{}); err != nil {
		log.Printf("Failed to migrate Admin table: %v", err)
	}

	adminUser := os.Getenv("ADMIN")
	adminPass := os.Getenv("ADMIN_PASSWORD")

	admin2User := os.Getenv("ADMIN2")
	admin2Pass := os.Getenv("ADMIN2_PASSWORD")

	type adminPair struct {
		username string
		password string
	}

	admins := []adminPair{
		{username: adminUser, password: adminPass},
		{username: admin2User, password: admin2Pass},
	}

	for _, ap := range admins {
		if ap.username == "" || ap.password == "" {
			continue
		}

		var admin models.Admin
		if err := db.Where("username = ?", ap.username).First(&admin).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				log.Printf("Creating default admin %s from environment...", ap.username)
				hashed, hashErr := bcrypt.GenerateFromPassword([]byte(ap.password), bcrypt.DefaultCost)
				if hashErr == nil {
					_ = db.Create(&models.Admin{
						Username: ap.username,
						Password: string(hashed),
					}).Error
				}
			}
		} else {
			// If already exists, skip (don't overwrite password).
		}
	}

	return db, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
