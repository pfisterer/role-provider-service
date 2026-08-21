package app

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/pfisterer/cloud-self-service-golib/envconf"
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
	// APITokens are the READ-ONLY Bearer tokens: they may query the graph but
	// not change it. Consumers (openstack-management-api, …) get one of these.
	APITokens []string `json:"api_tokens"`
	// APIWriteTokens may additionally create/modify groups, memberships and sync
	// sources. Empty means no token can write — writing the group graph means
	// writing authorization for every consumer, so it is opt-in, not a fallback.
	APIWriteTokens []string `json:"api_write_tokens"`
	// CORSAllowedOrigins lists the exact browser origins allowed to call this
	// API cross-origin. Empty (the default) allows none, which is correct here:
	// the consumers are services carrying an API token, not browsers.
	CORSAllowedOrigins []string `json:"cors_allowed_origins"`
	// ServiceTimeoutSeconds is the per-request context timeout.
	ServiceTimeoutSeconds int `json:"service_timeout_seconds"`
	// MaxResponseLimit is the global upper bound on paginated list endpoints (default 50).
	MaxResponseLimit int `json:"max_response_limit"`
	// GroupCacheRefreshSeconds is the background DRIFT-BACKSTOP interval of the
	// in-memory group search cache (default 600). The cache is refreshed
	// event-driven — on startup, after every sync, and after manual group
	// create/update/delete — so this ticker only guards against a missed/failed
	// event. <= 0 disables it entirely (pure event-driven).
	GroupCacheRefreshSeconds int `json:"group_cache_refresh_seconds"`
}

// loadAppConfiguration reads config from an optional .env file and environment variables.
func loadAppConfiguration() (AppConfiguration, error) {
	if _, err := os.Stat(".env"); err == nil {
		if err := godotenv.Overload(".env"); err != nil {
			return AppConfiguration{}, fmt.Errorf("failed to load .env: %w", err)
		}
	}

	tokens := envconf.StringSlice("API_TOKENS", nil)
	if len(tokens) == 0 {
		return AppConfiguration{}, fmt.Errorf("API_TOKENS must be set (comma-separated list of valid bearer tokens)")
	}

	cfg := AppConfiguration{
		DBType:                   envconf.String("DB_TYPE", "memory"),
		DBConnectionString:       envconf.String("DB_CONNECTION_STRING", "host=localhost user=postgres password=postgres dbname=group_auth_service port=5432 sslmode=disable TimeZone=UTC"),
		DBAddMockData:            envconf.String("DB_ADD_MOCK_DATA", "false") == "true",
		GinBindString:            envconf.String("API_BIND", ":8085"),
		DevMode:                  envconf.String("API_MODE", "production") == "development",
		APITokens:                tokens,
		APIWriteTokens:           envconf.StringSlice("API_WRITE_TOKENS", nil),
		CORSAllowedOrigins:       envconf.StringSlice("CORS_ALLOWED_ORIGINS", nil),
		ServiceTimeoutSeconds:    envconf.Int("SERVICE_TIMEOUT_SECONDS", 30),
		MaxResponseLimit:         envconf.Int("MAX_RESPONSE_LIMIT", 50),
		GroupCacheRefreshSeconds: envconf.Int("GROUP_CACHE_REFRESH_SECONDS", 600),
	}

	return cfg, nil
}
