package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pfisterer/cloud-self-service-golib/authn"
	"github.com/pfisterer/cloud-self-service-golib/oidcauth"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
	"github.com/pfisterer/role-provider-service/internal/storage"
	syncp "github.com/pfisterer/role-provider-service/internal/sync"
	"go.uber.org/zap"
)

// fakeVerifier accepts "id-<email>" as the ID token of that person, and "id-down" as a token nobody can judge right now.
type fakeVerifier struct{}

func (fakeVerifier) Verify(_ context.Context, raw string) (*authn.Claims, error) {
	if raw == "id-down" {
		return nil, oidcauth.ErrKeysUnavailable
	}
	if email, ok := strings.CutPrefix(raw, "id-"); ok {
		return &authn.Claims{Email: email}, nil
	}
	return nil, errors.New("not an id token")
}

func newManageServer(t *testing.T) testServer {
	t.Helper()
	log := zap.NewNop().Sugar()
	store := storage.NewMemoryStore(log)
	ctx := context.Background()
	local := &common.Source{ID: uuid.New(), Name: "ZWR", Type: common.SourceTypeLocal}
	if err := store.CreateSource(ctx, local); err != nil {
		t.Fatal(err)
	}
	relations, _ := common.NewRelations([]string{"dozent"})
	engine := syncp.NewEngine(store, relations, log)
	router, err := SetupRouter(SetupConfig{
		Log:              log,
		APITokens:        []string{readToken},
		APIWriteTokens:   []string{writeToken},
		GroupSvc:         groupmgmt.NewService(store, nil, relations, local.ID, 5*time.Second, log),
		Store:            store,
		SyncEngine:       engine,
		Scheduler:        syncp.NewScheduler(engine, store, log),
		MaxResponseLimit: 50,
		Verifier:         fakeVerifier{},
		AdminTokens:      []string{"group:root-admin"},
		LocalSourceName:  "ZWR",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := testServer{router: router, store: store}
	for _, body := range []string{`{"id":"root-admin"}`, `{"id":"wwi23seb","managers":["user:dekan@x"]}`, `{"id":"other"}`} {
		if w := s.do(http.MethodPost, "/v1/groups", writeToken, "application/json", []byte(body)); w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", body, w.Code, w.Body)
		}
	}
	if w := s.do(http.MethodPost, "/v1/groups/group:root-admin/members", writeToken, "application/json", []byte(`{"member":"user:root@x"}`)); w.Code != http.StatusNoContent {
		t.Fatalf("root admin: %d %s", w.Code, w.Body)
	}
	return s
}

func (s testServer) as(method, path, idToken, actAs, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+idToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if actAs != "" {
		req.Header.Set(authn.ActAsHeader, actAs)
	}
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// A person reaches /v1/manage and nothing else; a service token reaches everything but /v1/manage. The person's view of the graph is the groups they maintain, not anybody's tokens or any group's members.
func TestManageIsForPeopleOnly(t *testing.T) {
	s := newManageServer(t)
	for _, path := range []string{"/v1/users/root@x/tokens", "/v1/groups/group:root-admin/members", "/v1/groups"} {
		if w := s.as(http.MethodGet, path, "id-root@x", "", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("an ID token on %s: %d", path, w.Code)
		}
	}
	for _, token := range []string{readToken, writeToken} {
		if w := s.as(http.MethodGet, "/v1/manage/groups", token, "", ""); w.Code != http.StatusUnauthorized {
			t.Errorf("API token %s on /v1/manage: %d", token, w.Code)
		}
	}
	if w := s.as(http.MethodGet, "/v1/manage/groups", "id-down", "", ""); w.Code != http.StatusServiceUnavailable {
		t.Errorf("provider away: %d", w.Code)
	}
}

// What a person sees and may do follows from the groups they maintain, and an admin acting for them sees exactly that.
func TestManageAsManagerAdminAndOnBehalf(t *testing.T) {
	s := newManageServer(t)

	var me manageMeResponse
	w := s.as(http.MethodGet, "/v1/manage/me", "id-dekan@x", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || !me.ManagesAny || me.Admin || me.MayActAs || me.SourceName != "ZWR" {
		t.Errorf("dekan /me: %s", w.Body)
	}
	var groups []common.Group
	w = s.as(http.MethodGet, "/v1/manage/groups", "id-dekan@x", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &groups); err != nil || len(groups) != 1 || groups[0].ID != "wwi23seb" || groups[0].SourceName != "ZWR" {
		t.Errorf("dekan's groups: %s", w.Body)
	}

	expires := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	body := `{"member":"user:anna@x","relation":"dozent","expires_at":"` + expires + `"}`
	if w := s.as(http.MethodPost, "/v1/manage/groups/wwi23seb/members", "id-dekan@x", "", body); w.Code != http.StatusNoContent {
		t.Errorf("dekan adds: %d %s", w.Code, w.Body)
	}
	if w := s.as(http.MethodPost, "/v1/manage/groups/other/members", "id-dekan@x", "", `{"member":"user:anna@x"}`); w.Code != http.StatusForbidden {
		t.Errorf("dekan adds to a group they do not maintain: %d %s", w.Code, w.Body)
	}
	if w := s.as(http.MethodPost, "/v1/manage/groups", "id-dekan@x", "", `{"id":"neu"}`); w.Code != http.StatusForbidden {
		t.Errorf("dekan creates: %d %s", w.Code, w.Body)
	}

	var detail groupmgmt.GroupDetail
	w = s.as(http.MethodGet, "/v1/manage/groups/wwi23seb", "id-dekan@x", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil || len(detail.Memberships) != 1 || detail.Memberships[0].ExpiresAt == nil || detail.MayDelete {
		t.Errorf("detail: %s", w.Body)
	}

	// Somebody who maintains nothing is told so, not shown an error.
	w = s.as(http.MethodGet, "/v1/manage/me", "id-anna@x", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &me); err != nil || me.ManagesAny {
		t.Errorf("anna /me: %s", w.Body)
	}
	if w := s.as(http.MethodGet, "/v1/manage/me", "id-anna@x", "dekan@x", ""); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin acting on behalf: %d", w.Code)
	}

	// The root admin maintains everything and creates groups ...
	if w := s.as(http.MethodPost, "/v1/manage/groups", "id-root@x", "", `{"id":"neu","managers":["user:dekan@x"]}`); w.Code != http.StatusCreated {
		t.Errorf("admin creates: %d %s", w.Code, w.Body)
	}
	// ... and acting for the dekan sees the dekan's groups, and writes in the dekan's name with the admin recorded.
	w = s.as(http.MethodGet, "/v1/manage/groups", "id-root@x", "dekan@x", "")
	if err := json.Unmarshal(w.Body.Bytes(), &groups); err != nil || len(groups) != 2 {
		t.Errorf("acting for the dekan: %s", w.Body)
	}
	if w := s.as(http.MethodDelete, "/v1/manage/groups/wwi23seb/members?member=user:anna@x&relation=dozent", "id-root@x", "dekan@x", ""); w.Code != http.StatusNoContent {
		t.Errorf("remove on behalf: %d %s", w.Code, w.Body)
	}
	var history []common.Change
	w = s.as(http.MethodGet, "/v1/manage/groups/wwi23seb/history", "id-dekan@x", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || len(history) == 0 || history[0].Actor != "root@x" || history[0].OnBehalfOf != "dekan@x" || history[0].Action != common.ChangeMemberRemoved {
		t.Errorf("history: %s", w.Body)
	}
	if w := s.as(http.MethodDelete, "/v1/manage/groups/wwi23seb", "id-root@x", "dekan@x", ""); w.Code != http.StatusForbidden {
		t.Errorf("deleting while acting for a non-admin: %d", w.Code)
	}
	if w := s.as(http.MethodDelete, "/v1/manage/groups/neu", "id-root@x", "", ""); w.Code != http.StatusNoContent {
		t.Errorf("admin deletes: %d %s", w.Code, w.Body)
	}
}

// The local source is maintained through the group API, not as a sync source.
func TestLocalSourceIsNotASyncSource(t *testing.T) {
	s := newManageServer(t)
	sources, _ := s.store.ListSources(context.Background())
	id := sources[0].ID.String()
	if w := s.do(http.MethodDelete, "/v1/sync/sources/"+id, writeToken, "", nil); w.Code != http.StatusConflict {
		t.Errorf("delete: %d %s", w.Code, w.Body)
	}
	if w := s.do(http.MethodPut, "/v1/sync/sources/"+id, writeToken, "application/json", []byte(`{"name":"x"}`)); w.Code != http.StatusConflict {
		t.Errorf("update: %d %s", w.Code, w.Body)
	}
	if w := s.do(http.MethodPost, "/v1/sync/sources/"+id+"/trigger", writeToken, "", nil); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("trigger: %d %s", w.Code, w.Body)
	}
	// A reservation over a local group is refused.
	if w := s.do(http.MethodPost, "/v1/sync/sources", writeToken, "application/json", []byte(`{"name":"ad","type":"csv","reserved_names":["wwi*"]}`)); w.Code != http.StatusConflict {
		t.Errorf("reserve a taken name: %d %s", w.Code, w.Body)
	}
}

// A page from elsewhere cannot make the browser write here: the UI's proxy would turn the session cookie into a valid bearer.
func TestManageRefusesCrossOriginWrites(t *testing.T) {
	s := newManageServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1/manage/groups/wwi23seb/members", strings.NewReader(`{"member":"user:evil@x"}`))
	req.Header.Set("Authorization", "Bearer id-dekan@x")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("Origin", "https://evil.dhbw.cloud")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Errorf("cross-origin write: %d %s", w.Code, w.Body)
	}
}
