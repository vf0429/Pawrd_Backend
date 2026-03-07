package handlers

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/vf0429/Petwell_Backend/internal/models"
	"gorm.io/gorm"
)

// ScenarioResponse represents the JSON structure for GET /api/v1/scenarios
type ScenarioResponse struct {
	ID            string                   `json:"id"`
	Title         string                   `json:"title"`
	Description   string                   `json:"description"`
	TotalCostHKD  int                      `json:"total_cost_hkd"`
	CostBreakdown []models.CostItem        `json:"cost_breakdown"`
	Payouts       []ScenarioPayoutResponse `json:"payouts"`
}

type ScenarioPayoutResponse struct {
	InsurerID          string  `json:"insurer_id"`
	InsurerName        string  `json:"insurer_name"`
	PlanName           string  `json:"plan_name"`
	EstimatedPayoutHKD int     `json:"estimated_payout_hkd"`
	CoveragePercentage float64 `json:"coverage_percentage"`
	Analysis           string  `json:"analysis,omitempty"`
	IsRecommended      bool    `json:"is_recommended"`
}

func NewInsuranceV1Handler(db *gorm.DB) *gin.Engine {
	r := gin.Default()

	// Scenarios endpoints
	v1 := r.Group("/scenarios")
	{
		v1.GET("", func(c *gin.Context) {
			var scenarios []models.Scenario
			result := db.Preload("CostItems").Preload("Payouts").Preload("Payouts.Insurer").Find(&scenarios)
			if result.Error != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
				return
			}

			response := struct {
				Scenarios []ScenarioResponse `json:"scenarios"`
			}{
				Scenarios: []ScenarioResponse{}, // always return [] not null
			}

			for _, s := range scenarios {
				payouts := make([]ScenarioPayoutResponse, len(s.Payouts))
				for i, p := range s.Payouts {
					payouts[i] = ScenarioPayoutResponse{
						InsurerID:          p.InsurerID,
						InsurerName:        p.Insurer.Name,
						PlanName:           p.Insurer.PlanName,
						EstimatedPayoutHKD: p.EstimatedPayoutHKD,
						CoveragePercentage: p.CoveragePercentage,
						Analysis:           p.Analysis,
						IsRecommended:      p.IsRecommended,
					}
				}
				response.Scenarios = append(response.Scenarios, ScenarioResponse{
					ID:            s.ID,
					Title:         s.Title,
					Description:   s.Description,
					TotalCostHKD:  s.TotalCostHKD,
					CostBreakdown: s.CostItems,
					Payouts:       payouts,
				})
			}

			c.JSON(http.StatusOK, response)
		})

		v1.GET("/:id", func(c *gin.Context) {
			id := c.Param("id")
			var s models.Scenario
			result := db.Preload("CostItems").Preload("Payouts").Preload("Payouts.Insurer").First(&s, "id = ?", id)
			if result.Error != nil {
				if result.Error == gorm.ErrRecordNotFound {
					c.JSON(http.StatusNotFound, gin.H{"error": "Scenario not found"})
				} else {
					c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
				}
				return
			}

			payouts := make([]ScenarioPayoutResponse, len(s.Payouts))
			for i, p := range s.Payouts {
				payouts[i] = ScenarioPayoutResponse{
					InsurerID:          p.InsurerID,
					InsurerName:        p.Insurer.Name,
					PlanName:           p.Insurer.PlanName,
					EstimatedPayoutHKD: p.EstimatedPayoutHKD,
					CoveragePercentage: p.CoveragePercentage,
					Analysis:           p.Analysis,
					IsRecommended:      p.IsRecommended,
				}
			}

			response := ScenarioResponse{
				ID:            s.ID,
				Title:         s.Title,
				Description:   s.Description,
				TotalCostHKD:  s.TotalCostHKD,
				CostBreakdown: s.CostItems,
				Payouts:       payouts,
			}

			c.JSON(http.StatusOK, response)
		})
	}

	// Insurers endpoints
	insurers := r.Group("/insurers")
	{
		insurers.GET("", func(c *gin.Context) {
			var list []models.Insurer
			result := db.Find(&list)
			if result.Error != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": result.Error.Error()})
				return
			}
			c.JSON(http.StatusOK, gin.H{"insurers": list})
		})
	}

	return r
}
