package raggo

import (
	"fmt"
	"strconv"
	"strings"
)

type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

func ValidateProvider(provider string) (string, error) {
	normalized := normalizeOptionalString(provider)
	if normalized == "" {
		return "", nil
	}
	if !contains(SupportedProviders, normalized) {
		return "", &ValidationError{
			Code:    "invalid_provider",
			Message: "provider must be one of: " + strings.Join(SupportedProviders, ", "),
		}
	}
	return normalized, nil
}

func ValidateLanguage(language string) (string, error) {
	normalized := normalizeOptionalString(language)
	if normalized == "" {
		return "", nil
	}
	if !contains(SupportedLanguages, normalized) {
		return "", &ValidationError{
			Code:    "invalid_language",
			Message: "language must be one of: " + strings.Join(SupportedLanguages, ", "),
		}
	}
	return normalized, nil
}

func ValidateMaxSources(raw string, defaultMaxSources, maxAllowed int) (int, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return defaultMaxSources, nil
	}

	parsed, err := strconv.Atoi(text)
	if err != nil {
		return 0, &ValidationError{
			Code:    "invalid_max_sources",
			Message: fmt.Sprintf("max_sources must be an integer between 1 and %d", maxAllowed),
		}
	}
	if parsed < 1 || parsed > maxAllowed {
		return 0, &ValidationError{
			Code:    "invalid_max_sources",
			Message: fmt.Sprintf("max_sources must be between 1 and %d", maxAllowed),
		}
	}
	return parsed, nil
}

func BuildValidationErrorPayload(err *ValidationError, maxAllowedSources int) map[string]any {
	payload := map[string]any{
		"error":   err.Code,
		"message": err.Message,
	}
	if err.Code == "invalid_provider" {
		payload["supported_providers"] = SupportedProviders
	}
	if err.Code == "invalid_language" {
		payload["supported_languages"] = SupportedLanguages
	}
	if err.Code == "invalid_max_sources" {
		payload["max_allowed_sources"] = maxAllowedSources
	}
	return payload
}

func normalizeOptionalString(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
