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
