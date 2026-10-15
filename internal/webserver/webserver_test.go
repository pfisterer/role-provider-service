package webserver

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
	"github.com/pfisterer/role-provider-service/internal/storage"
	syncp "github.com/pfisterer/role-provider-service/internal/sync"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

const (
	readToken  = "read-token"
	writeToken = "write-token"
)

type testServer struct {
	router *gin.Engine
	store  storage.Store
	logs   *observer.ObservedLogs
}

func newTestServer(t *testing.T, store storage.Store, trustedProxies []string) testServer {
	t.Helper()
	core, logs := observer.New(zapcore.InfoLevel)
	log := zap.New(core).Sugar()
	relations, _ := common.NewRelations(nil)
	engine := syncp.NewEngine(store, relations, log)
	scheduler := syncp.NewScheduler(engine, store, log)
	router, err := SetupRouter(SetupConfig{
		Log:              log,
		APITokens:        []string{readToken},
		APIWriteTokens:   []string{writeToken},
		GroupSvc:         groupmgmt.NewService(store, nil, relations, 5*time.Second, log),
		Store:            store,
		SyncEngine:       engine,
		Scheduler:        scheduler,
		MaxResponseLimit: 50,
		TrustedProxies:   trustedProxies,
	})
	if err != nil {
		t.Fatal(err)
	}
	return testServer{router: router, store: store, logs: logs}
}

func (s testServer) do(method, path, token, contentType string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func multipartFile(t *testing.T, content []byte) (string, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "groups.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), buf.Bytes()
}

// A JSON body is capped at 1 MiB and an upload at 10 MiB; past that the server stops reading and answers 413.
func TestRequestBodyLimits(t *testing.T) {
	s := newTestServer(t, storage.NewMemoryStore(zap.NewNop().Sugar()), nil)

	big := []byte(`{"id":"kurs","description":"` + strings.Repeat("x", maxJSONBody) + `"}`)
	if w := s.do(http.MethodPost, "/v1/groups", writeToken, "application/json", big); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("JSON over the limit: %d %s", w.Code, w.Body)
	}
	if w := s.do(http.MethodPost, "/v1/groups", writeToken, "application/json", []byte(`{"id":"kurs"}`)); w.Code != http.StatusCreated {
		t.Errorf("small JSON: %d %s", w.Code, w.Body)
	}

	src := &common.Source{ID: uuid.New(), Name: "kurse", Type: common.SourceTypeCSV}
	if err := s.store.CreateSource(context.Background(), src); err != nil {
		t.Fatal(err)
	}
	path := "/v1/sync/sources/" + src.ID.String() + "/upload"

	// A file larger than the JSON limit but within the upload limit goes through.
	var csv strings.Builder
	csv.WriteString("group,member\n")
	for csv.Len() < 2*maxJSONBody {
		csv.WriteString("upload-ok,anna@dhbw.de\n")
	}
	ct, body := multipartFile(t, []byte(csv.String()))
	if w := s.do(http.MethodPost, path, writeToken, ct, body); w.Code != http.StatusOK {
		t.Errorf("upload within the limit: %d %s", w.Code, w.Body)
	}

	ct, body = multipartFile(t, bytes.Repeat([]byte("a"), maxUploadBody+1))
	if w := s.do(http.MethodPost, path, writeToken, ct, body); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("upload over the limit: %d %s", w.Code, w.Body)
	}
}

// brokenStore fails like a database whose message must not reach the client.
type brokenStore struct{ storage.Store }

const dbMessage = `pq: relation "sources" does not exist at character 15`

func (brokenStore) ListSources(context.Context) ([]common.Source, error) {
	return nil, errors.New(dbMessage)
}

func (brokenStore) ListGroups(context.Context, string, *uuid.UUID, int) ([]common.Group, error) {
	return nil, errors.New(dbMessage)
}

// A database error is answered with a generic 500 and logged in full.
func TestInternalErrorsAreNotReturned(t *testing.T) {
	s := newTestServer(t, brokenStore{storage.NewMemoryStore(zap.NewNop().Sugar())}, nil)
	for _, path := range []string{"/v1/sync/sources", "/v1/groups?source_id=" + uuid.NewString()} {
		w := s.do(http.MethodGet, path, readToken, "", nil)
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "pq:") {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if n := s.logs.FilterMessage("request failed").FilterField(zap.Error(errors.New(dbMessage))).Len(); n != 2 {
		t.Errorf("logged %d of 2 errors", n)
	}
}

// Invalid ids from the API are a 400 naming the problem, not a 500.
func TestInvalidIDIsBadRequest(t *testing.T) {
	s := newTestServer(t, storage.NewMemoryStore(zap.NewNop().Sugar()), nil)
	if w := s.do(http.MethodPost, "/v1/groups", writeToken, "application/json", []byte(`{"id":"root-admin,kurs-x"}`)); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "group id") {
		t.Errorf("create: %d %s", w.Code, w.Body)
	}
	s.do(http.MethodPost, "/v1/groups", writeToken, "application/json", []byte(`{"id":"kurs"}`))
	if w := s.do(http.MethodPost, "/v1/groups/group:kurs/members", writeToken, "application/json", []byte(`{"member":"pattern:*"}`)); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "pattern") {
		t.Errorf("add pattern: %d %s", w.Code, w.Body)
	}
}

// A schedule that cannot run is refused before the source is stored or changed.
func TestInvalidScheduleIsNotStored(t *testing.T) {
	s := newTestServer(t, storage.NewMemoryStore(zap.NewNop().Sugar()), nil)
	ctx := context.Background()

	w := s.do(http.MethodPost, "/v1/sync/sources", writeToken, "application/json", []byte(`{"name":"kurse","type":"csv","schedule":"every day"}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("create: %d %s", w.Code, w.Body)
	}
	if sources, _ := s.store.ListSources(ctx); len(sources) != 0 {
		t.Fatalf("a source with an invalid schedule was stored: %+v", sources)
	}

	src := &common.Source{ID: uuid.New(), Name: "kurse", Type: common.SourceTypeCSV, Schedule: "0 3 * * *"}
	if err := s.store.CreateSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	w = s.do(http.MethodPut, "/v1/sync/sources/"+src.ID.String(), writeToken, "application/json", []byte(`{"name":"kurse","schedule":"61 * * * *"}`))
	if w.Code != http.StatusBadRequest {
		t.Errorf("update: %d %s", w.Code, w.Body)
	}
	if got, _ := s.store.GetSource(ctx, src.ID); got.Schedule != "0 3 * * *" {
		t.Errorf("schedule changed to %q", got.Schedule)
	}

	w = s.do(http.MethodPut, "/v1/sync/sources/"+src.ID.String(), writeToken, "application/json", []byte(`{"name":"kurse","schedule":"*/5 * * * *"}`))
	if w.Code != http.StatusOK {
		t.Errorf("valid update: %d %s", w.Code, w.Body)
	}
}

// X-Forwarded-For is believed only from a configured proxy; otherwise any client could pick the address the logs record.
func TestClientIPIsNotSpoofable(t *testing.T) {
	cases := map[string]struct {
		proxies []string
		want    string
	}{
		"no proxy trusted":     {nil, "10.1.2.3"},
		"the peer is a proxy":  {[]string{"10.0.0.0/8"}, "203.0.113.7"},
		"another proxy listed": {[]string{"192.168.0.0/16"}, "10.1.2.3"},
	}
	for name, tc := range cases {
		s := newTestServer(t, storage.NewMemoryStore(zap.NewNop().Sugar()), tc.proxies)
		req := httptest.NewRequest(http.MethodGet, "/v1/groups", nil)
		req.RemoteAddr = "10.1.2.3:4567"
		req.Header.Set("Authorization", "Bearer wrong")
		req.Header.Set("X-Forwarded-For", "203.0.113.7")
		s.router.ServeHTTP(httptest.NewRecorder(), req)

		entries := s.logs.FilterMessage("rejected request with invalid API token").All()
		if len(entries) != 1 || entries[0].ContextMap()["remote_addr"] != tc.want {
			t.Errorf("%s: logged %+v, want remote_addr %s", name, entries, tc.want)
		}
	}
}

// Deleting a source's group through the API is a 409 that says where to change it.
func TestDeleteSourceGroupIsConflict(t *testing.T) {
	s := newTestServer(t, storage.NewMemoryStore(zap.NewNop().Sugar()), nil)
	src := uuid.New()
	if err := s.store.CreateGroup(context.Background(), &common.Group{ID: "imported", SourceID: &src}); err != nil {
		t.Fatal(err)
	}
	w := s.do(http.MethodDelete, "/v1/groups/group:imported", writeToken, "", nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "sync source") {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	s.do(http.MethodPost, "/v1/groups", writeToken, "application/json", []byte(`{"id":"manual"}`))
	if w := s.do(http.MethodDelete, "/v1/groups/group:manual", writeToken, "", nil); w.Code != http.StatusNoContent {
		t.Errorf("delete an API group: %d %s", w.Code, w.Body)
	}
}
