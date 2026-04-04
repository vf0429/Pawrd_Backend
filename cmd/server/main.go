package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/handlers"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/chat"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/places"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/rag"
)

var port = "8000"

func init() {
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}
}

func main() {
	// Load Configuration
	cfg := config.LoadConfig()
	ragClient := rag.NewClient(cfg)

	// Initialize chat session store (30-minute TTL)
	sessionStore := chat.NewSessionStore(30 * time.Minute)

	// Parse flags for seeding DB
	seedDB := flag.Bool("seed", false, "Seed the database with initial scenario data")
	flag.Parse()

	// Initialize DB
	db, err := models.InitDB(cfg)
	if err != nil {
		log.Fatalf("Fatal error initializing simple db: %v", err)
	}

	// Initialize auth database
	if err := models.InitAuthDB(); err != nil {
		log.Fatalf("Fatal error initializing auth db: %v", err)
	}

	if *seedDB {
		SeedDatabase(db)
		fmt.Println("Seeding complete. Exiting...")
		return
	}

	// Initialize new Gin router for scenarios API
	insuranceV1Router := handlers.NewInsuranceV1Handler(db)

	// Create a new mux
	mux := http.NewServeMux()

	// Media upload + static file serving
	uploadsDir := "assets/uploads"
	_ = os.MkdirAll(uploadsDir, 0755)
	mux.HandleFunc("/media/upload", handlers.NewMediaUploadHandler("http://localhost:8000"))
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(uploadsDir))))

	// Core handlers
	mux.HandleFunc("/vaccines", handlers.VaccinesHandler)
	mux.HandleFunc("/register", handlers.RegisterHandler)
	mux.HandleFunc("/posts", handlers.NewPostsHandler(db))

	// Seed test accounts on every startup (idempotent — skips existing)
	SeedTestAccounts()

	// Auth endpoints
	mux.HandleFunc("/api/auth/login", handlers.NewAuthLoginHandler())
	mux.HandleFunc("/api/auth/register", handlers.NewAuthRegisterHandler())
	mux.HandleFunc("/clinics", handlers.NewClinicsHandler(cfg))
	mux.HandleFunc("/emergency-clinics", handlers.NewEmergencyClinicsHandler(cfg))

	// Insurance handlers
	mux.HandleFunc("/insurance-companies", handlers.InsuranceCompaniesHandler)
	mux.HandleFunc("/insurance-products", handlers.InsuranceProductsHandler)
	mux.HandleFunc("/coverage-list", handlers.CoverageListHandler)
	mux.HandleFunc("/coverage-limits", handlers.CoverageLimitsHandler)
	mux.HandleFunc("/sub-coverage-limits", handlers.SubCoverageLimitsHandler)

	// Legacy handlers
	mux.HandleFunc("/insurance-providers", handlers.InsuranceProvidersHandler)
	mux.HandleFunc("/service-subcategories", handlers.ServiceSubcategoriesHandler)

	// Legacy RAG chat handler (now with session support)
	mux.HandleFunc("/api/chat", handlers.NewRAGHandler(ragClient, sessionStore))

	// New chat session endpoints
	mux.HandleFunc("/api/chat/session", handlers.NewChatSessionHandler(sessionStore))
	mux.HandleFunc("/api/chat/session/", handlers.NewChatSelectProviderHandler(sessionStore)) // matches /api/chat/session/{id}/provider
	mux.HandleFunc("/api/chat/providers", handlers.NewChatProvidersHandler(ragClient))
	mux.HandleFunc("/api/chat/ask", handlers.NewChatAskHandler(sessionStore, ragClient))

	// Vets handler
	placesClient := places.NewClient(cfg.MapsAPIKey)
	mux.HandleFunc("/api/vets", handlers.NewVetsHandler(placesClient))

	// Shop handlers
	mux.HandleFunc("/api/shop/products", handlers.NewShopHandler(cfg))
	mux.HandleFunc("/api/shop/products/{handle}", handlers.NewShopProductDetailHandler(cfg))
	mux.HandleFunc("/api/shop/categories", handlers.NewShopCategoriesHandler(cfg))
	mux.HandleFunc("/api/shop/search", handlers.NewShopSearchHandler(cfg))
	mux.HandleFunc("/api/shop/checkout/payment-sheet", handlers.NewShopPaymentSheetHandler(cfg))

	// Blog handlers
	mux.HandleFunc("/api/posts", handlers.NewBlogHandler(db))

	// Medical services handlers (public + admin)
	mux.HandleFunc("/api/medical/services", handlers.NewMedicalServicesHandler(db))
	mux.HandleFunc("/api/medical/services/{category}", handlers.NewMedicalServiceDetailHandler(db))
	mux.HandleFunc("/api/medical/admin/services/{id}", handlers.NewMedicalAdminUpdateHandler(db))

	// Health report extraction + fusion handlers (profile backend pipeline)
	mux.HandleFunc("/api/profile/storage/cos/presign-upload", handlers.NewCOSPresignUploadHandler())
	mux.HandleFunc("/api/profile/health-reports", handlers.NewHealthReportCreateHandler(db))
	mux.HandleFunc("/api/profile/health-reports/{id}", handlers.NewHealthReportDetailHandler(db))
	mux.HandleFunc("/api/profile/health-reports/observations/{observationId}/review", handlers.NewObservationReviewHandler(db))
	mux.HandleFunc("/api/profile/pets/{petId}/health", handlers.NewPetHealthProfileHandler(db))

	// Partner application handlers
	mux.HandleFunc("/api/partners/apply", handlers.NewPartnersApplyHandler(db))
	mux.HandleFunc("/api/admin/partners", handlers.NewPartnersAdminListHandler(db))
	partnerAction := handlers.NewPartnersAdminActionHandler(db)
	mux.HandleFunc("/api/admin/partners/{id}/approve", partnerAction)
	mux.HandleFunc("/api/admin/partners/{id}/reject", partnerAction)

	// Seed medical demo content on startup (idempotent)
	SeedMedicalServices(db)

	// Auto-seed scenario data if the table is empty (idempotent)
	var scenarioCount int64
	db.Model(&models.Scenario{}).Count(&scenarioCount)
	if scenarioCount == 0 {
		log.Println("No scenarios found, running auto-seed...")
		SeedDatabase(db)
	}

	// Mount Gin engine onto standard mux
	// We handle both /api/v1 and /api/v1/ to be safe
	v1Handler := http.StripPrefix("/api/v1", insuranceV1Router)
	mux.Handle("/api/v1", v1Handler)
	mux.Handle("/api/v1/", v1Handler)

	fmt.Printf("Pawrd Backend running at http://localhost:%s\n", port)
	fmt.Println("Chat endpoints:")
	fmt.Println("  POST /api/chat/session          - Create session")
	fmt.Println("  POST /api/chat/session/{id}/provider - Select provider")
	fmt.Println("  GET  /api/chat/providers         - List providers")
	fmt.Println("  POST /api/chat/ask               - Ask with context")

	err = http.ListenAndServe(":"+port, mux)
	if err != nil {
		fmt.Printf("Server failed to start: %v\n", err)
	}
}
