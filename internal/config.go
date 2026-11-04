package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/joho/godotenv"
	"github.com/pfisterer/cloud-self-service-golib/envconf"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/webserver"
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
	// SourceUploadTokens each let their holder upload to one sync source and do nothing else (SOURCE_UPLOAD_TOKENS, "<source>=<token>,…"), for a sync server outside the cluster that should not hold a write token.
	SourceUploadTokens []webserver.SourceUploadToken `json:"-"`
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
	// GroupRelations lists the relations a group can carry besides "member",
	// which is always there (e.g. "dozent,studierende"). An import or API call
	// with any other relation is refused rather than silently dropped.
	GroupRelations []string `json:"group_relations"`
	// TrustedProxies are the reverse proxies (addresses or CIDRs) whose X-Forwarded-For is believed for the client IP in the logs. Empty (the default) trusts none.
	TrustedProxies []string `json:"trusted_proxies"`
	// Keycloak configures the Keycloak source; an empty RealmURL disables it.
	Keycloak KeycloakConfig `json:"keycloak"`
	// LocalSourceName is the name of the source that holds the groups maintained here, shown next to them ("wwi23seb (ZWR)").
	LocalSourceName string `json:"local_source_name"`
	// AdoptSources names CSV/LDIF sources whose groups and memberships pass to the local source at startup, after which the source is deleted. Once done there is nothing left to adopt, so the setting can stay until it is removed.
	AdoptSources []string `json:"adopt_sources"`
	// OIDC verifies the people who maintain groups through the self-service UI; an empty IssuerURL leaves /v1/manage out.
	OIDC OIDCConfig `json:"oidc"`
	// AdminTokens maintain every local group, create and delete groups, and may act on behalf of another person (e.g. group:root-admin).
	AdminTokens []string `json:"admin_tokens"`
}

// OIDCConfig is the identity provider whose ID tokens /v1/manage accepts — the one the self-service UI signs people in with.
type OIDCConfig struct {
	IssuerURL string `json:"issuer_url"`
	ClientID  string `json:"client_id"`
	// JWKSURL, if set, spares the service from reaching the provider at startup (see oidcauth).
	JWKSURL string `json:"jwks_url"`
}

// KeycloakConfig is the connection to Keycloak and the rules that turn user
// attributes into groups (see internal/keycloak).
type KeycloakConfig struct {
	// RealmURL is the realm's issuer URL, e.g. https://sso.example/realms/main.
	RealmURL string `json:"realm_url"`
	ClientID string `json:"client_id"`
	// ClientSecret is the service account's secret; it needs
	// realm-management/view-users and nothing else.
	ClientSecret string `json:"-"`
	// Mapping is the JSON of keycloak.Mapping.
	Mapping string `json:"mapping"`
	// SyncSchedule is the cron schedule of the full read.
	SyncSchedule string `json:"sync_schedule"`
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
		GroupRelations:           envconf.StringSlice("GROUP_RELATIONS", nil),
		TrustedProxies:           envconf.StringSlice("TRUSTED_PROXIES", nil),
		Keycloak: KeycloakConfig{
			RealmURL:     envconf.String("KEYCLOAK_REALM_URL", ""),
			ClientID:     envconf.String("KEYCLOAK_CLIENT_ID", ""),
			ClientSecret: envconf.String("KEYCLOAK_CLIENT_SECRET", ""),
			Mapping:      envconf.String("KEYCLOAK_MAPPING", ""),
			SyncSchedule: envconf.String("KEYCLOAK_SYNC_SCHEDULE", "*/15 * * * *"),
		},
		LocalSourceName: envconf.String("LOCAL_SOURCE_NAME", "local"),
		AdoptSources:    envconf.StringSlice("ADOPT_SOURCES", nil),
		OIDC: OIDCConfig{
			IssuerURL: envconf.String("OIDC_ISSUER_URL", ""),
			ClientID:  envconf.String("OIDC_CLIENT_ID", ""),
			JWKSURL:   envconf.String("OIDC_JWKS_URL", ""),
		},
		AdminTokens: envconf.StringSlice("ADMIN_TOKENS", nil),
	}
	uploadTokens, err := parseSourceUploadTokens(envconf.StringSlice("SOURCE_UPLOAD_TOKENS", nil), cfg.APITokens, cfg.APIWriteTokens)
	if err != nil {
		return AppConfiguration{}, fmt.Errorf("SOURCE_UPLOAD_TOKENS: %w", err)
	}
	cfg.SourceUploadTokens = uploadTokens
	// An unknown backend is a typo, and falling back to the in-memory store would start a service that answers from empty data and forgets every write.
	if cfg.DBType != "memory" && cfg.DBType != "postgres" {
		return AppConfiguration{}, fmt.Errorf("DB_TYPE must be \"memory\" or \"postgres\", got %q", cfg.DBType)
	}
	if _, err := common.NewRelations(cfg.GroupRelations); err != nil {
		return AppConfiguration{}, fmt.Errorf("GROUP_RELATIONS: %w", err)
	}
	if cfg.OIDC.IssuerURL != "" && cfg.OIDC.ClientID == "" {
		return AppConfiguration{}, fmt.Errorf("OIDC_CLIENT_ID must be set with OIDC_ISSUER_URL")
	}
	for i, t := range cfg.AdminTokens {
		// Compared with resolved tokens, which are lowercase.
		cfg.AdminTokens[i] = common.NormalizeID(t)
	}

	return cfg, nil
}

// parseSourceUploadTokens reads "<source>=<token>" entries. A token that is also a read or write token would be both at once, and one shared by two sources would open both, so either is refused.
func parseSourceUploadTokens(entries, readTokens, writeTokens []string) ([]webserver.SourceUploadToken, error) {
	other := map[string]bool{}
	for _, t := range append(append([]string{}, readTokens...), writeTokens...) {
		other[t] = true
	}
	seen := map[string]bool{}
	var out []webserver.SourceUploadToken
	for _, e := range entries {
		source, token, ok := strings.Cut(e, "=")
		source, token = strings.TrimSpace(source), strings.TrimSpace(token)
		if !ok || source == "" || token == "" {
			return nil, fmt.Errorf("entry %d is not <source>=<token>", len(out)+1)
		}
		if other[token] {
			return nil, fmt.Errorf("the token for source %q is also a read or write token", source)
		}
		if seen[token] {
			return nil, fmt.Errorf("the token for source %q is used for another source too", source)
		}
		seen[token] = true
		out = append(out, webserver.SourceUploadToken{Source: source, Token: token})
	}
	return out, nil
}
