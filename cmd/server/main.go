package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/handlers"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/merchant"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/payments"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/places"
	"github.com/wangwuxing777/Pawrd_Backend/internal/services/shopify"
)

var port = "8000"

func init() {
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}
}

func bookingReconcileLoopConfig() (bool, time.Duration, int, string, bool) {
	secret := strings.TrimSpace(os.Getenv("BOOKING_SYNC_SHARED_SECRET"))
	if secret == "" {
		return false, 0, 0, "", false
	}
	rawInterval := strings.TrimSpace(os.Getenv("BOOKING_RECONCILE_INTERVAL_SECONDS"))
	if rawInterval == "" {
		return false, 0, 0, "", false
	}
	seconds, err := strconv.Atoi(rawInterval)
	if err != nil || seconds <= 0 {
		return false, 0, 0, "", false
	}
	limit := 50
	if rawLimit := strings.TrimSpace(os.Getenv("BOOKING_RECONCILE_LIMIT")); rawLimit != "" {
		if parsed, err := strconv.Atoi(rawLimit); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	syncState := strings.TrimSpace(strings.ToLower(os.Getenv("BOOKING_RECONCILE_SYNC_STATE")))
	if syncState != "" && syncState != "stale" && syncState != "never_synced" && syncState != "sync_error" {
		syncState = ""
	}
	force := strings.EqualFold(strings.TrimSpace(os.Getenv("BOOKING_RECONCILE_FORCE")), "true")
	return true, time.Duration(seconds) * time.Second, limit, syncState, force
}

func bookingFreshnessWindowConfig() time.Duration {
	raw := strings.TrimSpace(os.Getenv("BOOKING_MIRROR_FRESHNESS_SECONDS"))
	if raw == "" {
		return 2 * time.Minute
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 2 * time.Minute
	}
	return time.Duration(seconds) * time.Second
}

func startBookingReconcileLoop(listenPort string) {
	enabled, interval, limit, syncState, force := bookingReconcileLoopConfig()
	if !enabled {
		return
	}
	secret := strings.TrimSpace(os.Getenv("BOOKING_SYNC_SHARED_SECRET"))
	query := fmt.Sprintf("limit=%d", limit)
	if syncState != "" {
		query += "&sync_state=" + syncState
	}
	if force {
		query += "&force=true"
	}
	url := fmt.Sprintf("http://127.0.0.1:%s/api/bookings/reconcile-stale?%s", listenPort, query)
	go func() {
		log.Printf("Booking reconcile loop enabled: interval=%s limit=%d sync_state=%s force=%v", interval, limit, syncState, force)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		client := &http.Client{Timeout: 30 * time.Second}
		for range ticker.C {
			req, err := http.NewRequest(http.MethodPost, url, nil)
			if err != nil {
				log.Printf("Booking reconcile loop request build failed: %v", err)
				continue
			}
			req.Header.Set("X-Booking-Sync-Token", secret)
			req.Header.Set("X-Request-ID", "booking-reconcile-runner")
			resp, err := client.Do(req)
			if err != nil {
				log.Printf("Booking reconcile loop request failed: %v", err)
				continue
			}
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 300 {
				log.Printf("Booking reconcile loop non-2xx status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
			}
		}
	}()
}

func main() {
	// Load Configuration
	cfg := config.LoadConfig()
	if err := cfg.ValidateAuthConfig(); err != nil {
		log.Fatalf("Fatal authentication configuration error: %v", err)
	}
	if err := cfg.ValidateShopOperationalSecurity(); err != nil {
		log.Fatalf("Fatal shop operations security configuration error: %v", err)
	}
	if cfg.ShopCheckoutEnabled {
		if err := cfg.ValidateShopCheckoutConfig(); err != nil {
			log.Fatalf("Fatal shop checkout configuration error: %v", err)
		}
		log.Println("Shop checkout enabled with durable PostgreSQL storage.")
	} else {
		log.Println("Shop checkout disabled (set SHOP_CHECKOUT_ENABLED=true after configuring the complete payment pipeline).")
	}
	merchantVaccinationClient := merchant.NewClient(cfg)
	handlers.SetMirrorFreshnessWindow(bookingFreshnessWindowConfig())

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
	var shopifyAdmin shopify.AdminOrderClient
	var shopifyFulfillmentRequester shopify.AdminFulfillmentRequester
	var shopifyRefundMirrorClient shopify.AdminRefundMirrorClient
	if adminClient, adminErr := shopify.NewAdminClient(cfg); adminErr == nil {
		shopifyAdmin = adminClient
		shopifyFulfillmentRequester = adminClient
		shopifyRefundMirrorClient = adminClient
		if cfg.ShopifyWebhookCallbackURL != "" {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				created, err := adminClient.EnsureWebhookSubscriptions(ctx, cfg.ShopifyWebhookCallbackURL)
				if err != nil {
					log.Printf("Shopify webhook subscription sync failed: %v", err)
					return
				}
				log.Printf("Shopify webhook subscriptions ready: %d created", created)
			}()
		}
	} else if !cfg.UseMockShopify {
		log.Printf("Shopify Admin order operations unavailable: %v", adminErr)
	}
	orderDispatcher := payments.NewOrderDispatcher(db, shopifyAdmin)
	autoRequestingFulfiller := payments.NewAutoRequestingFulfiller(
		orderDispatcher,
		db,
		shopifyFulfillmentRequester,
		cfg.ShopifyAutoRequestFulfillment,
	)
	fulfillmentQueue := payments.NewDurableFulfillmentQueue(db, autoRequestingFulfiller)
	refundMirrorQueue := payments.NewDurableRefundMirrorQueue(db, shopifyRefundMirrorClient)
	var stripeRefunder payments.Refunder
	if stripeService, stripeErr := payments.NewStripeService(cfg); stripeErr == nil {
		stripeRefunder = stripeService
	} else {
		log.Printf("Stripe refund operations unavailable: %v", stripeErr)
	}
	compensationRefundQueue := payments.NewDurableCompensationRefundQueue(
		db,
		stripeRefunder,
		refundMirrorQueue,
	)

	if *seedDB {
		SeedDatabase(db)
		fmt.Println("Seeding complete. Exiting...")
		return
	}
	go fulfillmentQueue.Run(context.Background())
	go refundMirrorQueue.Run(context.Background())
	go compensationRefundQueue.Run(context.Background())

	// Initialize new Gin router for scenarios API
	insuranceV1Router := handlers.NewInsuranceV1Handler(db)

	// Create a new mux
	mux := http.NewServeMux()

	// Media upload + static file serving
	uploadsDir := "assets/uploads"
	thumbnailsDir := "assets/uploads/thumbs"
	_ = os.MkdirAll(uploadsDir, 0755)
	_ = os.MkdirAll(thumbnailsDir, 0755)
	publicBaseURL := os.Getenv("PUBLIC_BASE_URL")
	if publicBaseURL == "" {
		publicBaseURL = "http://localhost:" + port
	}
	mux.HandleFunc("/media/upload", handlers.NewMediaUploadHandler(publicBaseURL))
	mux.Handle("/uploads/", http.StripPrefix("/uploads/", http.FileServer(http.Dir(uploadsDir))))
	mux.HandleFunc("/rag-test", handlers.NewRAGTestPageHandler())

	// One-time migration: generate thumbnails for existing images
	MigrateImageThumbnails(db, publicBaseURL)

	// Core handlers
	mux.HandleFunc("/vaccines", handlers.VaccinesHandler)
	mux.HandleFunc("/register", handlers.RegisterHandler)
	mux.HandleFunc("/posts", handlers.NewPostsHandler(db))
	mux.HandleFunc("/posts/search", handlers.NewPostSearchHandler(db))
	mux.HandleFunc("/posts/hot-keywords", handlers.NewPostHotKeywordsHandler(db))
	mux.HandleFunc("/posts/{id}", handlers.NewPostDetailHandler(db))
	mux.HandleFunc("/posts/{id}/like", handlers.NewPostLikeHandler(db))
	mux.HandleFunc("/posts/{id}/collect", handlers.NewPostCollectHandler(db))
	mux.HandleFunc("/posts/{id}/comments", handlers.NewPostCommentsHandler(db))
	mux.HandleFunc("/posts/{id}/comments/{commentId}", handlers.NewCommentDeleteHandler(db))
	mux.HandleFunc("/posts/{id}/share", handlers.NewPostShareHandler(db))
	mux.HandleFunc("/posts/{id}/poll/vote", handlers.NewPostPollVoteHandler(db))
	mux.HandleFunc("/users/{id}/follow", handlers.NewUserFollowHandler(db))
	mux.HandleFunc("/users/{id}/followers", handlers.NewUserFollowersHandler(db))
	mux.HandleFunc("/users/{id}/following", handlers.NewUserFollowingHandler(db))
	mux.HandleFunc("/users/{id}/following-detail", handlers.NewUserFollowingDetailHandler(db))
	mux.HandleFunc("/users/{id}/followers-detail", handlers.NewUserFollowersDetailHandler(db))
	mux.HandleFunc("/users/{id}/stats", handlers.NewUserStatsHandler(db))
	mux.HandleFunc("/api/domain/families/me", handlers.NewFamilyProfileMeHandler(db))
	mux.HandleFunc("/api/domain/families/{idOrHandle}", handlers.NewFamilyProfileHandler(db))
	mux.HandleFunc("/api/domain/families/{idOrHandle}/follow", handlers.NewFamilyFollowHandler(db))
	mux.HandleFunc("/api/domain/families/{idOrHandle}/followers-detail", handlers.NewFamilyFollowersDetailHandler(db))
	mux.HandleFunc("/api/domain/family-owner/{ownerUserID}", handlers.NewFamilyProfileByOwnerHandler(db))
	mux.HandleFunc("/api/domain/pets/{slug}", handlers.NewPetProfileHandler(db))
	mux.HandleFunc("/api/domain/posts", handlers.NewPostPetTagsHandler(db))

	// Direct messages (1-on-1 chat)
	mux.HandleFunc("/messages/send", handlers.NewMessageSendHandler(db))
	mux.HandleFunc("/messages/conversations", handlers.NewConversationsHandler(db))
	mux.HandleFunc("/messages/thread", handlers.NewMessageThreadHandler(db))
	mux.HandleFunc("/messages/unread-count", handlers.NewMessagesUnreadCountHandler(db))
	mux.HandleFunc("/notifications", handlers.NewNotificationsHandler(db))
	mux.HandleFunc("/notifications/unread-count", handlers.NewNotificationsUnreadCountHandler(db))
	mux.HandleFunc("/notifications/read-all", handlers.NewNotificationsReadAllHandler(db))

	// Known-password demo users must never be created implicitly in a live
	// environment. Test deployments can opt in explicitly.
	if strings.EqualFold(strings.TrimSpace(os.Getenv("SEED_TEST_ACCOUNTS")), "true") {
		SeedTestAccounts()
	}
	EnsureDomainSeedData(db)

	// Auth endpoints
	mux.HandleFunc("/api/auth/login", handlers.NewAuthLoginHandler(db))
	mux.HandleFunc("/api/auth/register", handlers.NewAuthRegisterHandler(db))
	mux.HandleFunc("/api/auth/me", handlers.NewAuthMeHandler())
	mux.HandleFunc("/api/auth/verify/send", handlers.NewAuthVerifySendHandler())
	mux.HandleFunc("/api/auth/verify/check", handlers.NewAuthVerifyCheckHandler())
	mux.HandleFunc("/api/bookings", handlers.NewAppBookingsHandler(db, merchantVaccinationClient))
	mux.HandleFunc("/api/bookings/{bookingID}", handlers.NewAppBookingDetailHandler(db, merchantVaccinationClient))
	mux.HandleFunc("/api/bookings/sync", handlers.NewAppBookingSyncHandler(db, os.Getenv("BOOKING_SYNC_SHARED_SECRET")))
	mux.HandleFunc("/api/bookings/reconcile-stale", handlers.NewAppBookingReconcileHandler(db, merchantVaccinationClient, os.Getenv("BOOKING_SYNC_SHARED_SECRET")))
	mux.HandleFunc("/clinics", handlers.NewClinicsHandler(cfg))
	mux.HandleFunc("/emergency-clinics", handlers.NewEmergencyClinicsHandler(cfg))
	mux.HandleFunc("/api/maps/place-photo", handlers.NewPlacePhotoProxyHandler(cfg))

	// Insurance handlers
	mux.HandleFunc("/insurance-companies", handlers.InsuranceCompaniesHandler)
	mux.HandleFunc("/insurance-products", handlers.InsuranceProductsHandler)
	mux.HandleFunc("/coverage-list", handlers.CoverageListHandler)
	mux.HandleFunc("/coverage-limits", handlers.CoverageLimitsHandler)
	mux.HandleFunc("/sub-coverage-limits", handlers.SubCoverageLimitsHandler)

	// Legacy handlers
	mux.HandleFunc("/insurance-providers", handlers.InsuranceProvidersHandler)
	mux.HandleFunc("/service-subcategories", handlers.ServiceSubcategoriesHandler)

	// Chat compatibility proxy to the Python insurance RAG service.
	chatStore := handlers.NewChatSessionStore()
	mux.HandleFunc("/api/chat", handlers.NewChatProxyHandler(cfg, chatStore))
	mux.HandleFunc("/api/chat/session", handlers.NewChatSessionHandler(chatStore))
	mux.HandleFunc("/api/chat/session/{sessionID}/provider", handlers.NewChatSessionProviderHandler(chatStore))

	// Go direct-translation RAG shadow endpoints (parity migration track).
	mux.HandleFunc("/api/rag/go/query", handlers.NewGoRAGQueryHandler())
	mux.HandleFunc("/api/rag/go/capabilities", handlers.NewGoRAGCapabilitiesHandler())
	mux.HandleFunc("/api/rag/go/healthz", handlers.NewGoRAGHealthzHandler())
	mux.HandleFunc("/api/rag/go/readyz", handlers.NewGoRAGReadyzHandler())

	// Vets handler
	placesClient := places.NewClient(cfg.MapsAPIKey)
	mux.HandleFunc("/api/vets", handlers.NewVetsHandler(placesClient))

	// Shop handlers
	mux.HandleFunc("/api/shop/products", handlers.NewShopHandler(cfg))
	mux.HandleFunc("/api/shop/products/{handle}", handlers.NewShopProductDetailHandler(cfg))
	mux.HandleFunc("/api/shop/categories", handlers.NewShopCategoriesHandler(cfg))
	mux.HandleFunc("/api/shop/search", handlers.NewShopSearchHandler(cfg))
	mux.HandleFunc("/api/shop/checkout/quote", handlers.NewShopQuoteHandler(cfg, db))
	mux.HandleFunc("/api/shop/checkout/payment-sheet", handlers.NewShopPaymentSheetHandler(cfg, db))
	mux.HandleFunc("/api/payments/webhook", handlers.NewPaymentsWebhookHandler(cfg, db, fulfillmentQueue, refundMirrorQueue))
	mux.HandleFunc("/api/shop/webhooks/shopify", handlers.NewShopifyWebhookHandler(cfg, db))
	mux.HandleFunc("/api/shop/orders", handlers.NewShopOrdersHandler(db))
	mux.HandleFunc("/api/shop/orders/{orderID}", handlers.NewShopOrderDetailHandler(db, shopifyAdmin))
	mux.HandleFunc("/api/shop/orders/{orderID}/received", handlers.NewShopOrderReceivedHandler(db, shopifyAdmin))
	mux.HandleFunc("/api/shop/orders/{orderID}/return-request", handlers.NewShopOrderReturnHandler(db, shopifyAdmin))
	mux.HandleFunc("/api/admin/shop/orders/{orderID}/refund", handlers.NewShopOrderRefundHandler(db, stripeRefunder, cfg.ShopAdminKey, refundMirrorQueue))
	mux.HandleFunc("/api/admin/shop/orders/{orderID}/request-fulfillment", handlers.NewShopOrderFulfillmentRequestHandler(db, shopifyFulfillmentRequester, cfg.ShopAdminKey))

	// HiCustom (custom products) handlers — designer entry + design persistence.
	mux.HandleFunc("/api/shop/hicustom/designer-url", handlers.NewHiCustomDesignerURLHandler(cfg))
	mux.HandleFunc("/api/shop/hicustom/designs", handlers.NewHiCustomDesignCreateHandler(cfg, db))
	mux.HandleFunc("/api/shop/hicustom/designs/{id}", handlers.NewHiCustomDesignDetailHandler(db))

	// Medical services handlers (public + admin)
	mux.HandleFunc("/api/medical/services", handlers.NewMedicalServicesHandler(db))
	mux.HandleFunc("/api/medical/services/{category}", handlers.NewMedicalServiceDetailHandler(db))
	mux.HandleFunc("/api/medical/admin/services/{id}", handlers.NewMedicalAdminUpdateHandler(db))
	mux.HandleFunc("/api/medical/vaccinations/availability", handlers.NewVaccinationAvailabilityProxyHandler(merchantVaccinationClient))
	mux.HandleFunc("/api/medical/vaccinations/bookings", handlers.NewVaccinationBookingCreateProxyHandler(merchantVaccinationClient))
	mux.HandleFunc("/api/medical/vaccinations/bookings/{externalBookingID}", handlers.NewVaccinationBookingGetProxyHandler(merchantVaccinationClient))
	mux.HandleFunc("/api/medical/vaccinations/bookings/{externalBookingID}/cancel", handlers.NewVaccinationBookingCancelProxyHandler(merchantVaccinationClient))

	// Health report extraction + fusion handlers (profile backend pipeline)
	mux.HandleFunc("/api/profile/storage/cos/presign-upload", handlers.NewCOSPresignUploadHandler())
	mux.HandleFunc("/api/profile/health-reports", handlers.NewHealthReportCreateHandler(db))
	mux.HandleFunc("/api/profile/health-reports/{id}", handlers.NewHealthReportDetailHandler(db))
	mux.HandleFunc("/api/profile/health-reports/observations/{observationId}/review", handlers.NewObservationReviewHandler(db))
	mux.HandleFunc("/api/profile/pets/{petId}/health", handlers.NewPetHealthProfileHandler(db))
	mux.HandleFunc("/api/profile/pets/{petId}/share-grants", handlers.NewPetAccessGrantsHandler(db))
	mux.HandleFunc("/api/profile/pets/{petId}/share-grants/{grantId}/revoke", handlers.NewPetAccessGrantRevokeHandler(db))
	mux.HandleFunc("/api/share/{token}", handlers.NewShareResolveHandler(db))

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

	startBookingReconcileLoop(port)

	fmt.Printf("PetWell Backend running at http://localhost:%s\n", port)
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	err = server.ListenAndServe()
	if err != nil {
		fmt.Printf("Server failed to start: %v\n", err)
	}
}
