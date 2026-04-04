package handlers

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	// "time" // Removed unused

	"github.com/wangwuxing777/Pawrd_Backend/internal/config"
	"github.com/wangwuxing777/Pawrd_Backend/internal/models"
	"googlemaps.github.io/maps"
)

type ClinicsService struct {
	clinics []models.Clinic
	mu      sync.RWMutex
}

var (
	serviceInstance *ClinicsService
	serviceOnce     sync.Once
)

func getClinicsService(cfg *config.Config) *ClinicsService {
	serviceOnce.Do(func() {
		serviceInstance = &ClinicsService{}
		// Load in background or blocking? Blocking is safer for initial load if data is small.
		serviceInstance.loadClinics(cfg)
	})
	return serviceInstance
}

func (s *ClinicsService) loadClinics(cfg *config.Config) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Adjust path if running from root
	path := filepath.Join("assets", "clinics.csv")
	f, err := os.Open(path)
	if err != nil {
		// Try looking in ../data if running from cmd/...
		path = filepath.Join("..", "assets", "clinics.csv")
		f, err = os.Open(path)
		if err != nil {
			fmt.Printf("Error opening clinics.csv: %v\n", err)
			return
		}
	}
	defer f.Close()

	reader := csv.NewReader(f)
	records, err := reader.ReadAll()
	if err != nil {
		fmt.Printf("Error reading CSV: %v\n", err)
		return
	}

	var mapsClient *maps.Client
	if cfg.MapsAPIKey != "" {
		c, err := maps.NewClient(maps.WithAPIKey(cfg.MapsAPIKey))
		if err == nil {
			mapsClient = c
		} else {
			fmt.Printf("Maps client error: %v\n", err)
		}
	}

	var clinics []models.Clinic
	// Skip header
	if len(records) > 0 {
		records = records[1:]
	}

	for _, record := range records {
		if len(record) < 13 {
			continue
		}

		c := models.Clinic{
			ClinicID:       record[0],
			Name:           record[1],
			Address:        record[2],
			PhoneRegular:   record[3],
			PhoneEmergency: record[4],
			Whatsapp:       record[5],
			OpeningHours:   record[6],
			Emergency24h:   record[7],
			WebsiteURL:     record[8],
			ApplemapURL:    record[9],
			Latitude:       record[10],
			Longitude:      record[11],
			Rating:         record[12],
		}

		if len(record) > 13 {
			c.GooglePlaceID = record[13]
		}
		if len(record) > 14 {
			c.PhotoReference = record[14]
			if c.PhotoReference != "" && cfg.MapsAPIKey != "" {
				c.PhotoURL = fmt.Sprintf("https://maps.googleapis.com/maps/api/place/photo?maxwidth=800&photo_reference=%s&key=%s", c.PhotoReference, cfg.MapsAPIKey)
			}
		}

		clinics = append(clinics, c)
	}

	// Enrich with Google Maps
	if mapsClient != nil {
		fmt.Println("Enriching clinics with Google Maps data in background...")
		// Launch background task to enrich
		go func() {
			s.mu.Lock()
			// Create a copy of pointers or iterate over indices?
			// Range over slice copies elements, range over index is safer.
			// But modifying slice elements in place requires locking for race conditions if read concurrently.
			// The API handler holds RLock. So we need Lock to write.
			// To avoid blocking reads for too long, we can lock per clinic update.
			count := len(s.clinics)
			s.mu.Unlock()

			// Create a rate limiter channel, e.g., 5 concurrent requests
			sem := make(chan struct{}, 5)
			var wg sync.WaitGroup

			for i := 0; i < count; i++ {
				// Acquire semaphore
				wg.Add(1)
				sem <- struct{}{}

				go func(idx int) {
					defer func() { <-sem; wg.Done() }()

					// Read clinic safely (need read lock for name/address, but since these are strings and immutable, maybe OK without lock if no one writes?)
					// Actually we are the only writer.
					// Readers might read while we read. RLock needed.
					s.mu.RLock()
					cl := s.clinics[idx] // Copy the struct value
					s.mu.RUnlock()

					// photo_reference can expire. If place_id exists, we should still refresh details.
					// Only skip when we already have local data but cannot refresh from Google (no place_id).
					if cl.GooglePlaceID == "" && cl.Latitude != "" && cl.Longitude != "" && cl.PhotoReference != "" {
						return
					}

					var candidate *maps.PlacesSearchResult
					var err error

					// If we only have name/address, search
					if cl.GooglePlaceID == "" {
						// Search for place
						r := &maps.FindPlaceFromTextRequest{
							Input:     fmt.Sprintf("%s %s", cl.Name, cl.Address),
							InputType: maps.FindPlaceFromTextInputTypeTextQuery,
							Fields: []maps.PlaceSearchFieldMask{
								maps.PlaceSearchFieldMaskPlaceID,
							},
						}

						resp, err := mapsClient.FindPlaceFromText(context.Background(), r)
						if err == nil && len(resp.Candidates) > 0 {
							candidate = &resp.Candidates[0]
						} else {
							// Try just name if name + address fails?
							// fmt.Printf("Failed to find: %s (%v)\n", cl.Name, err)
							return
						}
					} else {
						// If we have ID but missing other info (e.g. photo), we might need to fetch details directly
						// But for simplicity, let's just assume if ID is missing we start over.
					}

					placeID := ""
					if candidate != nil {
						placeID = candidate.PlaceID
					} else {
						placeID = cl.GooglePlaceID
					}

					if placeID == "" {
						return
					}

					// Now get full details
					details, err := mapsClient.PlaceDetails(context.Background(), &maps.PlaceDetailsRequest{
						PlaceID: placeID,
						// Fetch all fields by default to avoid undefined constant errors
					})

					if err != nil {
						fmt.Printf("Failed to get details for %s: %v\n", cl.Name, err)
						return
					}

					s.mu.Lock()
					// Re-read or just update directly
					// We update the struct in the slice
					target := &s.clinics[idx]

					target.GooglePlaceID = placeID

					if target.Latitude == "" || target.Longitude == "" {
						target.Latitude = fmt.Sprintf("%f", details.Geometry.Location.Lat)
						target.Longitude = fmt.Sprintf("%f", details.Geometry.Location.Lng)
					}

					if details.Rating != 0 {
						target.Rating = fmt.Sprintf("%.1f", details.Rating)
					}

					if details.InternationalPhoneNumber != "" {
						target.PhoneRegular = details.InternationalPhoneNumber
					}

					if details.Website != "" {
						target.WebsiteURL = details.Website
					}

					// Process Opening Hours
					if details.OpeningHours != nil && len(details.OpeningHours.WeekdayText) > 0 {
						// Fill opening hours if empty
						if target.OpeningHours == "" {
							target.OpeningHours = strings.Join(details.OpeningHours.WeekdayText, "; ")
						}

						is24h := false
						for _, dayText := range details.OpeningHours.WeekdayText {
							lowerText := strings.ToLower(dayText)
							if strings.Contains(lowerText, "open 24 hours") || strings.Contains(lowerText, "24-hour") {
								is24h = true
								break
							}
						}
						// Only update if true, otherwise keep existing value or set to FALSE?
						// User asked to judge if true. If CSV has FALSE but google says TRUE, we update to TRUE.
						// If CSV has TRUE but google says FALSE, maybe we should respect Google?
						// For safety let's just mark TRUE if we find it.
						if is24h {
							target.Emergency24h = "TRUE"
						} else {
							// Optional: verified not 24h?
							// target.Emergency24h = "FALSE"
						}
					}

					// Process Photos
					if len(details.Photos) > 0 {
						ref := details.Photos[0].PhotoReference
						if ref != "" {
							target.PhotoReference = ref
							target.PhotoURL = fmt.Sprintf("https://maps.googleapis.com/maps/api/place/photo?maxwidth=800&photo_reference=%s&key=%s", ref, cfg.MapsAPIKey)
						}
					}

					s.mu.Unlock()
					fmt.Printf("Enriched: %s (Rating: %.1f)\n", cl.Name, details.Rating)
				}(i)
			}

			wg.Wait()
			s.saveClinics()
		}()
	}

	s.clinics = clinics
	fmt.Printf("Loaded %d clinics from CSV\n", len(clinics))
}

func (s *ClinicsService) saveClinics() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Adjust path if running from root
	path := filepath.Join("assets", "clinics.csv")
	// If running inside cmd/server, adjust
	if _, err := os.Stat(path); os.IsNotExist(err) {
		path = filepath.Join("..", "assets", "clinics.csv")
	}

	f, err := os.Create(path)
	if err != nil {
		fmt.Printf("Error creating CSV for save: %v\n", err)
		return
	}
	defer f.Close()

	writer := csv.NewWriter(f)
	defer writer.Flush()

	// Header
	header := []string{
		"clinic_id", "name", "address", "phone_regular", "phone_emergency", "whatsapp",
		"opening_hours", "emergency_24h", "website_url", "applemap_url", "latitude", "longitude",
		"rating", "google_place_id", "photo_reference",
	}
	if err := writer.Write(header); err != nil {
		fmt.Printf("Error writing header: %v\n", err)
		return
	}

	for _, c := range s.clinics {
		record := []string{
			c.ClinicID,
			c.Name,
			c.Address,
			c.PhoneRegular,
			c.PhoneEmergency,
			c.Whatsapp,
			c.OpeningHours,
			c.Emergency24h,
			c.WebsiteURL,
			c.ApplemapURL,
			c.Latitude,
			c.Longitude,
			c.Rating,
			c.GooglePlaceID,
			c.PhotoReference,
		}
		if err := writer.Write(record); err != nil {
			fmt.Printf("Error writing record: %v\n", err)
		}
	}
	fmt.Printf("Saved updated clinics data to %s\n", path)
}

func NewClinicsHandler(cfg *config.Config) http.HandlerFunc {
	svc := getClinicsService(cfg)
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		w.Header().Set("Content-Type", "application/json")

		svc.mu.RLock()
		defer svc.mu.RUnlock()

		if err := json.NewEncoder(w).Encode(svc.clinics); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}

func NewEmergencyClinicsHandler(cfg *config.Config) http.HandlerFunc {
	svc := getClinicsService(cfg)
	return func(w http.ResponseWriter, r *http.Request) {
		EnableCors(&w)
		w.Header().Set("Content-Type", "application/json")

		svc.mu.RLock()
		defer svc.mu.RUnlock()

		var filtered []models.Clinic
		for _, c := range svc.clinics {
			if strings.EqualFold(c.Emergency24h, "true") {
				filtered = append(filtered, c)
			}
		}

		if err := json.NewEncoder(w).Encode(filtered); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
