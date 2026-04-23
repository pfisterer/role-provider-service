package app

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/pfisterer/role-provider-service/internal/helper"
)

// AppConfiguration is the top-level service configuration.
type AppConfiguration struct {
	// DBType selects the storage backend: "memory" (default) or "postgres".
	DBType string `json:"db_type"`
	// DBConnectionString is the DSN for the PostgreSQL database (only used when DBType is "postgres").
	DBConnectionString string `json:"db_connection_string"`
	// DBAddMockData seeds the store with development mock data on startup.
	DBAddMockData bool `json:"db_add_mock_data"`
	// API bind address, e.g. ":8085".
	GinBindString string `json:"gin_bind_string"`
	// DevMode enables debug logging and disables HTTP caching.
	DevMode bool `json:"dev_mode"`
	// APITokens is the list of valid Bearer tokens.
	APITokens []string `json:"api_tokens"`
	// ServiceTimeoutSeconds is the per-request context timeout.
	ServiceTimeoutSeconds int `json:"service_timeout_seconds"`
	// MaxResponseLimit is the global upper bound on paginated list endpoints (default 50).
	MaxResponseLimit int `json:"max_response_limit"`
}

// loadAppConfiguration reads config from an optional .env file and environment variables.
func loadAppConfiguration() (AppConfiguration, error) {
	if _, err := os.Stat(".env"); err == nil {
		if err := godotenv.Overload(".env"); err != nil {
			return AppConfiguration{}, fmt.Errorf("failed to load .env: %w", err)
		}
	}

	tokens := helper.GetEnvStringSlice("API_TOKENS", nil)
	if len(tokens) == 0 {
		return AppConfiguration{}, fmt.Errorf("API_TOKENS must be set (comma-separated list of valid bearer tokens)")
	}

	cfg := AppConfiguration{
		DBType:                helper.GetEnvString("DB_TYPE", "memory"),
		DBConnectionString:    helper.GetEnvString("DB_CONNECTION_STRING", "host=localhost user=postgres password=postgres dbname=group_auth_service port=5432 sslmode=disable TimeZone=UTC"),
		DBAddMockData:         helper.GetEnvString("DB_ADD_MOCK_DATA", "false") == "true",
		GinBindString:         helper.GetEnvString("API_BIND", ":5"),
		DevMode:               helper.GetEnvString("API_MODE", "production") == "development",
		APITokens:             tokens,
		ServiceTimeoutSeconds: helper.GetEnvInt("SERVICE_TIMEOUT_SECONDS", 30),
		MaxResponseLimit:      helper.GetEnvInt("MAX_RESPONSE_LIMIT", 50),
	}

	return cfg, nil
}
