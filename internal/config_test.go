package app

import (
	"strings"
	"testing"
)

// A typo in DB_TYPE must not start a service on an empty in-memory store that forgets every write.
func TestUnknownDBTypeIsAnError(t *testing.T) {
	t.Setenv("API_TOKENS", "read-token")
	for _, typ := range []string{"postgress", "Postgres", "sqlite"} {
		t.Setenv("DB_TYPE", typ)
		if _, err := loadAppConfiguration(); err == nil || !strings.Contains(err.Error(), "DB_TYPE") {
			t.Errorf("%q: got %v", typ, err)
		}
	}
	for _, typ := range []string{"memory", "postgres"} {
		t.Setenv("DB_TYPE", typ)
		if _, err := loadAppConfiguration(); err != nil {
			t.Errorf("%q: %v", typ, err)
		}
	}
}

func TestSourceUploadTokens(t *testing.T) {
	t.Setenv("API_TOKENS", "read-token")
	t.Setenv("API_WRITE_TOKENS", "write-token")

	t.Setenv("SOURCE_UPLOAD_TOKENS", "dualis=upload-1, other = upload-2")
	cfg, err := loadAppConfiguration()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SourceUploadTokens) != 2 || cfg.SourceUploadTokens[0].Source != "dualis" || cfg.SourceUploadTokens[1].Token != "upload-2" {
		t.Errorf("got %+v", cfg.SourceUploadTokens)
	}

	for _, bad := range []string{"dualis", "=upload-1", "dualis=", "dualis=write-token", "dualis=read-token", "a=same,b=same"} {
		t.Setenv("SOURCE_UPLOAD_TOKENS", bad)
		if _, err := loadAppConfiguration(); err == nil || !strings.Contains(err.Error(), "SOURCE_UPLOAD_TOKENS") {
			t.Errorf("%q: got %v", bad, err)
		}
	}
}

// The dev sign-in only exists in development mode and never next to a real identity provider; anything else refuses to start instead of ignoring the variable.
func TestDevAuthUser(t *testing.T) {
	t.Setenv("API_TOKENS", "read-token")
	t.Setenv("DEV_AUTH_USER", " Root.Admin@uni.example ")

	t.Setenv("API_MODE", "production")
	if _, err := loadAppConfiguration(); err == nil || !strings.Contains(err.Error(), "DEV_AUTH_USER") {
		t.Errorf("production: got %v", err)
	}

	t.Setenv("API_MODE", "development")
	cfg, err := loadAppConfiguration()
	if err != nil || cfg.DevAuthUser != "root.admin@uni.example" {
		t.Errorf("development: %q %v", cfg.DevAuthUser, err)
	}

	t.Setenv("OIDC_ISSUER_URL", "https://sso.example/realms/main")
	t.Setenv("OIDC_CLIENT_ID", "ui")
	if _, err := loadAppConfiguration(); err == nil || !strings.Contains(err.Error(), "DEV_AUTH_USER") {
		t.Errorf("with OIDC: got %v", err)
	}
}
