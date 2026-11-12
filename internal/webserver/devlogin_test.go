package webserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/cloud-self-service-golib/authn"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
	"github.com/pfisterer/role-provider-service/internal/storage"
	syncp "github.com/pfisterer/role-provider-service/internal/sync"
	"go.uber.org/zap"
)

const devUser = "root.admin@uni.example"

// devSetup is the local development setup: the mock data, adopted into the local source as at startup, and the given router settings on top.
func devSetup(t *testing.T, cfg SetupConfig) (SetupConfig, *groupmgmt.Service) {
	t.Helper()
	log := zap.NewNop().Sugar()
	store := storage.NewMemoryStore(log)
	ctx := context.Background()
	if err := storage.SeedMockData(ctx, store, log); err != nil {
		t.Fatal(err)
	}
	local := &common.Source{ID: uuid.New(), Name: "local", Type: common.SourceTypeLocal}
	if err := store.CreateSource(ctx, local); err != nil {
		t.Fatal(err)
	}
	relations, _ := common.NewRelations([]string{"dozent", "studierende"})
	svc := groupmgmt.NewService(store, nil, relations, local.ID, 5*time.Second, log)
	if _, err := svc.Adopt(ctx, groupmgmt.Caller{Actor: "system", Admin: true}, nil, "test"); err != nil {
		t.Fatal(err)
	}
	engine := syncp.NewEngine(store, relations, log)
	cfg.Log = log
	cfg.APITokens = []string{readToken}
	cfg.APIWriteTokens = []string{writeToken}
	cfg.GroupSvc = svc
	cfg.Store = store
	cfg.SyncEngine = engine
	cfg.Scheduler = syncp.NewScheduler(engine, store, log)
	cfg.MaxResponseLimit = 50
	cfg.LocalSourceName = "local"
	return cfg, svc
}

func newDevServer(t *testing.T, adminTokens []string) testServer {
	t.Helper()
	cfg, _ := devSetup(t, SetupConfig{DevMode: true, DevAuthUser: " Root.Admin@uni.example ", AdminTokens: adminTokens})
	router, err := SetupRouter(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return testServer{router: router, store: cfg.Store}
}

// devRequest is a request as the UI sends it through the Vite proxy with dummy auth: no Authorization header, same origin.
func (s testServer) devRequest(method, path, actAs, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if actAs != "" {
		req.Header.Set(authn.ActAsHeader, actAs)
	}
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Origin", "http://localhost:8084")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

func groupIDs(t *testing.T, w *httptest.ResponseRecorder) []string {
	t.Helper()
	var page groupmgmt.GroupPage
	if w.Code != http.StatusOK {
		t.Fatalf("groups: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, g := range page.Items {
		ids = append(ids, g.ID)
	}
	slices.Sort(ids)
	return ids
}

// The dev sign-in can never be switched on outside development mode, nor next to a real identity provider.
func TestDevLoginRefusedOutsideDevMode(t *testing.T) {
	for name, cfg := range map[string]SetupConfig{
		"production":      {DevMode: false, DevAuthUser: devUser},
		"with a verifier": {DevMode: true, DevAuthUser: devUser, Verifier: fakeVerifier{}},
	} {
		full, _ := devSetup(t, cfg)
		if _, err := SetupRouter(full); err == nil {
			t.Errorf("%s: dev sign-in accepted", name)
		}
	}
}

// Without a verifier and without a dev user /v1/manage does not exist, in development mode too — and an empty dev user is no dev user.
func TestManageAbsentWithoutVerifierOrDevUser(t *testing.T) {
	for _, devMode := range []bool{false, true} {
		cfg, _ := devSetup(t, SetupConfig{DevMode: devMode, DevAuthUser: "  "})
		router, err := SetupRouter(cfg)
		if err != nil {
			t.Fatal(err)
		}
		s := testServer{router: router}
		if w := s.devRequest(http.MethodGet, "/v1/manage/me", "", ""); w.Code != http.StatusNotFound {
			t.Errorf("dev mode %v: /v1/manage/me %d", devMode, w.Code)
		}
	}
}

// In development a request without a bearer is the dev user, resolved like a verified sign-in: admin through ADMIN_TOKENS, acting on behalf with X-Act-As. The API-token routes stay as they are.
func TestDevLoginManage(t *testing.T) {
	s := newDevServer(t, []string{"group:root_uni"})

	var me manageMeResponse
	w := s.devRequest(http.MethodGet, "/v1/manage/me", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || me.Actor != devUser || !me.Admin || !me.MayActAs || !me.ManagesAny {
		t.Fatalf("/me: %d %s", w.Code, w.Body)
	}

	// The mock data gives an admin something to browse: the local groups, several sources, nested groups.
	if ids := groupIDs(t, s.devRequest(http.MethodGet, "/v1/manage/groups", "", "")); !slices.Contains(ids, "cloud_lab") || len(ids) < 8 {
		t.Errorf("local groups: %v", ids)
	}
	var sources []groupmgmt.SourceSummary
	w = s.devRequest(http.MethodGet, "/v1/manage/sources", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &sources); err != nil || len(sources) != 3 || !sources[0].Local || sources[0].Groups == 0 {
		t.Errorf("sources: %d %s", w.Code, w.Body)
	}
	if ids := groupIDs(t, s.devRequest(http.MethodGet, "/v1/manage/groups?source=LDAP", "", "")); !slices.Equal(ids, []string{"staff"}) {
		t.Errorf("top of LDAP: %v", ids)
	}
	if ids := groupIDs(t, s.devRequest(http.MethodGet, "/v1/manage/groups?parent=cloud_lab", "", "")); !slices.Equal(ids, []string{"cloud_lab_tutors", "staff-cs"}) {
		t.Errorf("in cloud_lab: %v", ids)
	}

	// Writes from the UI's own origin pass.
	if w := s.devRequest(http.MethodPost, "/v1/manage/groups", "", `{"id":"dev-new"}`); w.Code != http.StatusCreated {
		t.Errorf("create: %d %s", w.Code, w.Body)
	}

	// Acting on behalf: the person's own view, no admin.
	w = s.devRequest(http.MethodGet, "/v1/manage/me", "faculty@cs.example", "")
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || me.Admin || me.ActingAs != "faculty@cs.example" || !me.ManagesAny {
		t.Errorf("/me on behalf: %d %s", w.Code, w.Body)
	}
	if ids := groupIDs(t, s.devRequest(http.MethodGet, "/v1/manage/groups", "faculty@cs.example", "")); !slices.Equal(ids, []string{"cloud_lab_tutors", "cs_seminar", "thesis_2026"}) {
		t.Errorf("faculty's groups: %v", ids)
	}

	// A bearer is never taken for the dev user, and there is nothing to verify it with.
	req := httptest.NewRequest(http.MethodGet, "/v1/manage/me", nil)
	req.Header.Set("Authorization", "Bearer "+writeToken)
	w = httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("bearer on dev /v1/manage: %d", w.Code)
	}

	// The rest of /v1 still wants an API token.
	if w := s.devRequest(http.MethodGet, "/v1/groups", "", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("/v1/groups without a token: %d", w.Code)
	}
	if w := s.do(http.MethodGet, "/v1/groups", readToken, "", nil); w.Code != http.StatusOK {
		t.Errorf("/v1/groups with a read token: %d", w.Code)
	}
}

// Without ADMIN_TOKENS the dev user is an ordinary person and still maintains groups of the mock data.
func TestDevLoginWithoutAdminTokens(t *testing.T) {
	s := newDevServer(t, nil)
	var me manageMeResponse
	w := s.devRequest(http.MethodGet, "/v1/manage/me", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || me.Admin || me.MayActAs || !me.ManagesAny {
		t.Fatalf("/me: %d %s", w.Code, w.Body)
	}
	if ids := groupIDs(t, s.devRequest(http.MethodGet, "/v1/manage/groups", "", "")); !slices.Equal(ids, []string{"cloud_lab", "cloud_lab_leads", "cloud_lab_tutors", "thesis_2026"}) {
		t.Errorf("dev user's groups: %v", ids)
	}
	if w := s.devRequest(http.MethodGet, "/v1/manage/me", "faculty@cs.example", ""); w.Code != http.StatusForbidden {
		t.Errorf("acting on behalf without being an admin: %d %s", w.Code, w.Body)
	}
}
