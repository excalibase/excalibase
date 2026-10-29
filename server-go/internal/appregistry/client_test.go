package appregistry

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

// fakeRegistry serves a catalog and tag lists a page at a time and records
// the grants each request carried.
type fakeRegistry struct {
	t       *testing.T
	issuer  *Issuer
	repos   []string
	tags    map[string][]string
	deleted []string
	grants  map[string][]Grant
	status  int
}

func (f *fakeRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var claims parsedClaims
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if _, err := jwt.ParseWithClaims(raw, &claims, func(*jwt.Token) (any, error) { return f.issuer.key.Public(), nil }); err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	f.grants[r.Method+" "+r.URL.Path] = claims.Access
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v2/")
	switch {
	case path == "_catalog":
		page(w, r, "repositories", f.repos)
	case strings.HasSuffix(path, "/tags/list"):
		repo := strings.TrimSuffix(path, "/tags/list")
		tags, ok := f.tags[repo]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"code":"NAME_UNKNOWN"}]}`))
			return
		}
		page(w, r, "tags", tags)
	case r.Method == http.MethodDelete && strings.Contains(path, "/manifests/"):
		f.deleted = append(f.deleted, path)
		w.WriteHeader(http.StatusAccepted)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func page(w http.ResponseWriter, r *http.Request, key string, items []string) {
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	start := 0
	if last := r.URL.Query().Get("last"); last != "" {
		start = slices.Index(items, last) + 1
	}
	end := min(start+n, len(items))
	_ = json.NewEncoder(w).Encode(map[string]any{key: items[start:end], "name": "proj-a/api"})
}

func fakeClient(t *testing.T, f *fakeRegistry) *Client {
	t.Helper()
	issuer, _ := testIssuer(t)
	f.t, f.issuer, f.grants = t, issuer, map[string][]Grant{}
	server := httptest.NewServer(f)
	t.Cleanup(server.Close)
	client, err := NewClient(server.URL, issuer)
	if err != nil {
		t.Fatal(err)
	}
	client.pageSize = 2
	return client
}

func TestClient_ListsEveryPageWithTheNarrowestToken(t *testing.T) {
	f := &fakeRegistry{repos: []string{"proj-a/api", "proj-a/web", "proj-b/api"}, tags: map[string][]string{"proj-a/api": {"p1-a", "p2-b", "p3-c"}}}
	client := fakeClient(t, f)
	repos, err := client.Repositories(context.Background())
	if err != nil || !slices.Equal(repos, f.repos) {
		t.Fatalf("repositories %v %v", repos, err)
	}
	if got := f.grants["GET /v2/_catalog"]; len(got) != 1 || got[0].Name != "" || !slices.Equal(got[0].Actions, []string{"pull"}) {
		t.Errorf("catalog grant %+v", got)
	}
	tags, err := client.Tags(context.Background(), "proj-a/api")
	if err != nil || !slices.Equal(tags, f.tags["proj-a/api"]) {
		t.Fatalf("tags %v %v", tags, err)
	}
	if got := f.grants["GET /v2/proj-a/api/tags/list"]; len(got) != 1 || got[0].Name != "proj-a/api" || !slices.Equal(got[0].Actions, []string{"pull"}) {
		t.Errorf("tags grant %+v", got)
	}
	if tags, err := client.Tags(context.Background(), "proj-a/web"); err != nil || len(tags) != 0 {
		t.Errorf("a repository with nothing in it: %v %v", tags, err)
	}
}

func TestClient_DeletesOneDigestWithADeleteTokenOnItsRepository(t *testing.T) {
	f := &fakeRegistry{}
	client := fakeClient(t, f)
	digest := "sha256:" + strings.Repeat("cd", 32)
	if err := client.Delete(context.Background(), "proj-a/api", digest); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.deleted, []string{"proj-a/api/manifests/" + digest}) {
		t.Errorf("deleted %v", f.deleted)
	}
	if got := f.grants["DELETE /v2/proj-a/api/manifests/"+digest]; len(got) != 1 || got[0].Name != "proj-a/api" || !slices.Equal(got[0].Actions, []string{"delete"}) {
		t.Errorf("delete grant %+v", got)
	}
	if err := client.Delete(context.Background(), "proj-a/api", "latest"); err == nil {
		t.Error("a delete by tag")
	}
	if err := client.Delete(context.Background(), "../proj-a", digest); err == nil {
		t.Error("a delete outside a project's repository")
	}
}

func TestClient_ReportsWhatTheRegistryRefuses(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError, http.StatusFound} {
		f := &fakeRegistry{status: status}
		client := fakeClient(t, f)
		if _, err := client.Repositories(context.Background()); err == nil {
			t.Errorf("%d: catalog read", status)
		}
		if _, err := client.Tags(context.Background(), "proj-a/api"); err == nil {
			t.Errorf("%d: tags read", status)
		}
		if err := client.Delete(context.Background(), "proj-a/api", "sha256:"+strings.Repeat("cd", 32)); err == nil {
			t.Errorf("%d: delete", status)
		}
	}
	f := &fakeRegistry{status: http.StatusNotFound}
	client := fakeClient(t, f)
	if err := client.Delete(context.Background(), "proj-a/api", "sha256:"+strings.Repeat("cd", 32)); err != nil {
		t.Errorf("a digest already gone: %v", err)
	}
}

func TestNewClient_RefusesWhatIsNotAnHTTPRegistry(t *testing.T) {
	issuer, _ := testIssuer(t)
	for _, raw := range []string{"", "ftp://zot:5000", "http://", "http://zot:5000/v2", "http://user:pw@zot:5000", "zot:5000"} {
		if _, err := NewClient(raw, issuer); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
	if _, err := NewClient("http://zot:5000", nil); err == nil {
		t.Error("a client that cannot sign")
	}
	if _, err := NewClient(fmt.Sprintf("http://app-registry.ns.svc.cluster.local:%d/", 5000), issuer); err != nil {
		t.Errorf("the chart's address: %v", err)
	}
}
