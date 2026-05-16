package config

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/joho/godotenv"
)

// Config holds runtime configuration loaded from environment variables.
type Config struct {
	LLMApiKey        string // LLM_API_KEY — required, non-empty
	LLMBaseURL       string // LLM_BASE_URL — required, must start with "http"
	LLMModel         string // LLM_MODEL — required
	GoogleCredsJSON  string // GOOGLE_CREDENTIALS_JSON - optional (takes precedence for PaaS like Railway)
	GoogleCredsPath  string // GOOGLE_APPLICATION_CREDENTIALS — required if JSON is empty
	SheetsID         string // SHEETS_SPREADSHEET_ID — required
	WASessionDBPath  string // WHATSAPP_SESSION_DB_PATH — required
	OwnerPhoneNumber []string // OWNER_PHONE_NUMBER — required, digits only, min 10 chars, comma-separated list 
	AllowedGroupJIDs []string // ALLOWED_GROUP_JIDS — optional, comma-separated group JID user parts
}

// Load reads configuration from .env/environment variables and validates them.
func Load() (*Config, error) {
	// Intentionally ignore error so env vars can still come from process env
	// when .env is absent (e.g., production/deployment).
	// Use Overload so test-provided .env values can replace existing env values.
	_ = godotenv.Overload()

	cfg := &Config{
		LLMApiKey:        strings.TrimSpace(os.Getenv("LLM_API_KEY")),
		LLMBaseURL:       strings.TrimSpace(os.Getenv("LLM_BASE_URL")),
		LLMModel:         strings.TrimSpace(os.Getenv("LLM_MODEL")),
		GoogleCredsJSON:  strings.TrimSpace(os.Getenv("GOOGLE_CREDENTIALS_JSON")),
		GoogleCredsPath:  strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")),
		SheetsID:         strings.TrimSpace(os.Getenv("SHEETS_SPREADSHEET_ID")),
		WASessionDBPath:  strings.TrimSpace(os.Getenv("WHATSAPP_SESSION_DB_PATH")),
		OwnerPhoneNumber: parseCommaSeparated(os.Getenv("OWNER_PHONE_NUMBER")),
		AllowedGroupJIDs: parseCommaSeparated(os.Getenv("ALLOWED_GROUP_JIDS")),
	}

	if err := validate(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func validate(cfg *Config) error {
	required := map[string]string{
		"LLM_API_KEY":              cfg.LLMApiKey,
		"LLM_BASE_URL":             cfg.LLMBaseURL,
		"LLM_MODEL":                cfg.LLMModel,
		"SHEETS_SPREADSHEET_ID":    cfg.SheetsID,
		"WHATSAPP_SESSION_DB_PATH": cfg.WASessionDBPath,
	}

	if len(cfg.OwnerPhoneNumber) == 0 {
		return fmt.Errorf("missing required env var: OWNER_PHONE_NUMBER")
	}

	for key, value := range required {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("missing required env var: %s", key)
		}
	}

	if !strings.HasPrefix(strings.ToLower(cfg.LLMBaseURL), "http") {
		return fmt.Errorf("invalid LLM_BASE_URL: must start with \"http\"")
	}

	if cfg.GoogleCredsJSON == "" && cfg.GoogleCredsPath == "" {
		return fmt.Errorf("missing either GOOGLE_CREDENTIALS_JSON or GOOGLE_APPLICATION_CREDENTIALS")
	}

	if cfg.GoogleCredsJSON == "" {
		if _, err := os.Stat(cfg.GoogleCredsPath); err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("invalid GOOGLE_APPLICATION_CREDENTIALS: file does not exist: %s", cfg.GoogleCredsPath)
			}
			return fmt.Errorf("invalid GOOGLE_APPLICATION_CREDENTIALS: %w", err)
		}
	}

	for _, number := range cfg.OwnerPhoneNumber {
		if len(number) < 10 {
			return fmt.Errorf("invalid OWNER_PHONE_NUMBER (%s): must be at least 10 digits", number)
		}
		for _, r := range number {
			if !unicode.IsDigit(r) {
				return fmt.Errorf("invalid OWNER_PHONE_NUMBER (%s): must contain digits only", number)
			}
		}
	}

	return nil
}

func parseCommaSeparated(s string) []string {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
