package roleclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Both consumers rely on this round trip: the request editor carries the bearer token, the query reaches the service, and a group's relations come back decoded (aiaas offers them as "group:<id>#<relation>").
func TestListGroupsRoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/groups" || r.URL.Query().Get("q") != "cs" {
			t.Errorf("unexpected request %s", r.URL)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":"cs","display_name":"CS","relations":["dozent"]}]`))
	}))
	defer srv.Close()

	c, err := NewClientWithResponses(srv.URL, WithRequestEditorFn(func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer secret")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	q := "cs"
	resp, err := c.ListGroupsWithResponse(context.Background(), &ListGroupsParams{Q: &q})
	if err != nil {
		t.Fatal(err)
	}
	if resp.JSON200 == nil || len(*resp.JSON200) != 1 {
		t.Fatalf("status %d, body %s", resp.StatusCode(), resp.Body)
	}
	g := (*resp.JSON200)[0]
	if g.Id == nil || *g.Id != "cs" || g.Relations == nil || len(*g.Relations) != 1 || (*g.Relations)[0] != "dozent" {
		t.Fatalf("decoded %+v", g)
	}
}
