package main

import (
	"log"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/joho/godotenv"
	"github.com/yourorg/whatsapp-broadcast/internal/handlers"
	"github.com/yourorg/whatsapp-broadcast/internal/models"
	"github.com/yourorg/whatsapp-broadcast/internal/scheduler"
	"github.com/yourorg/whatsapp-broadcast/internal/storage"
	"github.com/yourorg/whatsapp-broadcast/internal/whatsapp"
)

func main() {
	// Load .env in development
	_ = godotenv.Load()

	// Connect DB
	db, err := storage.Connect()
	if err != nil {
		log.Fatalf("DB connection failed: %v", err)
	}

	// Auto-migrate tables
	if err := db.AutoMigrate(
		&models.Broadcast{},
		&models.Patient{},
		&models.MessageLog{},
	); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}

	// Init WhatsApp client
	waClient := whatsapp.NewClient(db)
	if err := waClient.Connect(); err != nil {
		log.Printf("WhatsApp connect warning: %v", err)
	}

	// Init scheduler
	sched := scheduler.New(db, waClient)
	sched.Start()
	defer sched.Stop()

	// Fiber app
	app := fiber.New(fiber.Config{
		BodyLimit: 20 * 1024 * 1024, // 20MB for Excel uploads
	})

	app.Use(logger.New())
	app.Use(cors.New(cors.Config{
		AllowOrigins: getAllowedOrigins(),
		AllowHeaders: "Origin, Content-Type, Accept, Authorization",
		AllowMethods: "GET, POST, PUT, DELETE, OPTIONS",
	}))

	// Static files for uploaded excels
	app.Static("/uploads", "./uploads")

	// Register routes
	h := handlers.New(db, waClient, sched)
	h.RegisterRoutes(app)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Printf("Server running on :%s", port)
	// log.Fatal(app.Listen(":" + port))
	log.Fatal(app.Listen("0.0.0.0:8080"))
}

func getAllowedOrigins() string {
	origin := os.Getenv("ALLOWED_ORIGINS")
	if origin == "" {
		return "http://localhost:3000,http://localhost:5173"
	}
	return origin
}
