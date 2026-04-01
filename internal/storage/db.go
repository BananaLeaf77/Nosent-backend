package storage

import (
	"fmt"
	"log"
	"os"
	"strconv"

	"github.com/Nosent/whatsapp-broadcast/internal/models"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func Connect() (*gorm.DB, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
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

	if err := db.AutoMigrate(&models.Admin{}); err != nil {
		log.Printf("Failed to migrate Admin table: %v", err)
	}

	seedAdmins(db)

	return db, nil
}

// seedAdmins reads ADMIN_COUNT (default 2) from env, then looks for
// ADMIN_1 / ADMIN_1_PASSWORD … ADMIN_N / ADMIN_N_PASSWORD pairs.
// For backwards-compat it also accepts the legacy ADMIN / ADMIN_PASSWORD
// and ADMIN2 / ADMIN2_PASSWORD pairs (mapped to index 1 and 2).
func seedAdmins(db *gorm.DB) {
	countStr := getEnv("ADMIN_COUNT", "")
	count := 0

	if countStr != "" {
		n, err := strconv.Atoi(countStr)
		if err != nil || n < 0 {
			log.Printf("[seed] ADMIN_COUNT invalid, defaulting to auto-detect")
		} else {
			count = n
		}
	}

	type pair struct {
		username string
		password string
	}

	var admins []pair

	if count > 0 {
		// Numbered mode: ADMIN_1, ADMIN_2, … ADMIN_N
		for i := 1; i <= count; i++ {
			u := os.Getenv(fmt.Sprintf("ADMIN_%d", i))
			p := os.Getenv(fmt.Sprintf("ADMIN_%d_PASSWORD", i))
			if u != "" && p != "" {
				admins = append(admins, pair{u, p})
			}
		}
	} else {
		// Auto-detect mode: keep reading ADMIN_1..ADMIN_99 until one is empty,
		// then fall back to legacy ADMIN / ADMIN2 style.
		for i := 1; i <= 99; i++ {
			u := os.Getenv(fmt.Sprintf("ADMIN_%d", i))
			p := os.Getenv(fmt.Sprintf("ADMIN_%d_PASSWORD", i))
			if u == "" || p == "" {
				break
			}
			admins = append(admins, pair{u, p})
		}

		// Legacy fallback: ADMIN / ADMIN_PASSWORD (no number)
		if len(admins) == 0 {
			legacyPairs := []pair{
				{os.Getenv("ADMIN"), os.Getenv("ADMIN_PASSWORD")},
				{os.Getenv("ADMIN2"), os.Getenv("ADMIN2_PASSWORD")},
			}
			for _, lp := range legacyPairs {
				if lp.username != "" && lp.password != "" {
					admins = append(admins, lp)
				}
			}
		}
	}

	for _, ap := range admins {
		var admin models.Admin
		err := db.Where("username = ?", ap.username).First(&admin).Error
		if err != nil {
			hashed, hashErr := bcrypt.GenerateFromPassword([]byte(ap.password), bcrypt.DefaultCost)
			if hashErr != nil {
				log.Printf("[seed] failed to hash password for %s: %v", ap.username, hashErr)
				continue
			}
			if createErr := db.Create(&models.Admin{
				Username: ap.username,
				Password: string(hashed),
			}).Error; createErr != nil {
				log.Printf("[seed] failed to create admin %s: %v", ap.username, createErr)
			} else {
				log.Printf("[seed] created admin: %s", ap.username)
			}
		}
		// If already exists, leave password untouched.
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}