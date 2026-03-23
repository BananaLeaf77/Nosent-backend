package storage

import (
	"fmt"
	"log"
	"os"

	"github.com/yourorg/whatsapp-broadcast/internal/models"
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

	if adminUser != "" && adminPass != "" {
		var admin models.Admin
		if err := db.Where("username = ?", adminUser).First(&admin).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				log.Printf("Creating default admin from environment...")
				hashed, err := bcrypt.GenerateFromPassword([]byte(adminPass), bcrypt.DefaultCost)
				if err == nil {
					db.Create(&models.Admin{
						Username: adminUser,
						Password: string(hashed),
					})
				}
			}
		} else {
			// If already exists, you can theoretically update password if it changes in env,
			// but prompt says "if already exist skip"
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
