package providercatalog

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Provider struct {
	ID      string
	Name    string
	HasData bool
}

type definition struct {
	ID      string
	Name    string
	Aliases []string
}

var knownDefinitions = []definition{
	{
		ID:      "bluecross",
		Name:    "Blue Cross 藍十字",
		Aliases: []string{"bluecross", "blue_cross", "blue cross", "藍十字"},
	},
	{
		ID:      "MSIG",
		Name:    "MSIG",
		Aliases: []string{"msig"},
	},
	{
		ID:      "one_degree",
		Name:    "OneDegree",
		Aliases: []string{"one_degree", "onedegree", "one degree"},
	},
	{
		ID:      "prudential",
		Name:    "Prudential 保誠",
		Aliases: []string{"prudential", "保誠", "pruchoice"},
	},
}

func KnownProviders() []Provider {
	providers := make([]Provider, 0, len(knownDefinitions))
	for _, provider := range knownDefinitions {
		providers = append(providers, Provider{
			ID:   provider.ID,
			Name: provider.Name,
		})
	}
	return providers
}

func DetectProvider(query string) string {
	providers := DetectProviders(query)
	if len(providers) > 0 {
		return providers[0]
	}
	return ""
}

func DetectProviders(query string) []string {
	lower := strings.ToLower(query)
	out := make([]string, 0, len(knownDefinitions))
	seen := map[string]struct{}{}
	for _, provider := range knownDefinitions {
		for _, alias := range provider.Aliases {
			if strings.Contains(lower, alias) {
				if _, ok := seen[provider.ID]; !ok {
					seen[provider.ID] = struct{}{}
					out = append(out, provider.ID)
				}
				break
			}
		}
	}
	return out
}

func NormalizeProviderID(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	if value == "" || value == "string" || value == "all" || value == "all providers" {
		return ""
	}
	for _, provider := range knownDefinitions {
		if value == provider.ID {
			return provider.ID
		}
		for _, alias := range provider.Aliases {
			if value == alias {
				return provider.ID
			}
		}
	}
	return value
}

func DisplayName(providerID string) string {
	for _, provider := range knownDefinitions {
		if provider.ID == providerID {
			return provider.Name
		}
	}
	return providerID
}

func BuildProviderList(dataPath string) []Provider {
	discovered := discoverProviders(dataPath)
	providerIDs := make(map[string]struct{}, len(knownDefinitions)+len(discovered))
	for _, provider := range knownDefinitions {
		providerIDs[provider.ID] = struct{}{}
	}
	for providerID := range discovered {
		providerIDs[providerID] = struct{}{}
	}

	ids := make([]string, 0, len(providerIDs))
	for providerID := range providerIDs {
		ids = append(ids, providerID)
	}
	sort.Strings(ids)

	providers := make([]Provider, 0, len(ids))
	for _, providerID := range ids {
		providers = append(providers, Provider{
			ID:      providerID,
			Name:    DisplayName(providerID),
			HasData: discovered[providerID],
		})
	}
	return providers
}

func discoverProviders(dataPath string) map[string]bool {
	found := make(map[string]bool)
	if strings.TrimSpace(dataPath) == "" {
		return found
	}

	entries, err := os.ReadDir(dataPath)
	if err != nil {
		return found
	}

	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		providerID := filepath.Base(entry.Name())
		if !isKnownProvider(providerID) {
			continue
		}
		found[providerID] = true
	}

	return found
}

func isKnownProvider(providerID string) bool {
	for _, provider := range knownDefinitions {
		if provider.ID == providerID {
			return true
		}
	}
	return false
}
