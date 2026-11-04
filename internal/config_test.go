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
