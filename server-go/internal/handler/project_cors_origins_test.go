package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// AddCorsOrigin, RemoveCorsOrigin and ReleaseAppCorsOrigin make memCorsStore
// a ProjectCorsEditor; owner records which app added which origin.
func (m *memCorsStore) AddCorsOrigin(_ context.Context, projectID, origin, appID string) (bool, []string, error) {
	if m.err != nil {
		return false, nil, m.err
	}
	current := m.origins[projectID]
	if domain.IsCorsWildcard(current) || slices.Contains(current, origin) {
		return false, current, nil
	}
	next, err := domain.ParseCorsOrigins(append(slices.Clone(current), origin), false)
	if err != nil {
		return false, nil, err
	}
	m.origins[projectID] = next
	if appID != "" {
		m.appOrigins()[projectID+"/"+appID] = origin
	}
	return true, next, nil
}

func (m *memCorsStore) RemoveCorsOrigin(_ context.Context, projectID, origin string) (bool, []string, error) {
	if m.err != nil {
		return false, nil, m.err
	}
	current := m.origins[projectID]
	index := slices.Index(current, origin)
	if index < 0 {
		return false, current, nil
	}
	m.origins[projectID] = slices.Delete(slices.Clone(current), index, index+1)
	return true, m.origins[projectID], nil
}

func (m *memCorsStore) ReleaseAppCorsOrigin(ctx context.Context, projectID, appID string) (string, error) {
	origin, ok := m.appOrigins()[projectID+"/"+appID]
	if !ok {
		return "", m.err
	}
	delete(m.owner, projectID+"/"+appID)
	removed, _, err := m.RemoveCorsOrigin(ctx, projectID, origin)
	if err != nil || !removed {
		return "", err
	}
	return origin, nil
}

func (m *memCorsStore) appOrigins() map[string]string {
	if m.owner == nil {
		m.owner = map[string]string{}
	}
	return m.owner
}

type corsOriginAnswer struct {
	corsResponse
	Added   bool `json:"added"`
	Removed bool `json:"removed"`
}

func TestCorsOrigins_AddKeepsWhatIsThereAndRecordsTheApp(t *testing.T) {
	f := setupCorsHandler(t)
	f.cors.origins[testCorsProject] = []string{testCorsOriginB}
	w := doJSON(f.router, http.MethodPost, testCorsPath+"/origins", map[string]string{"origin": " HTTPS://App.Example.com:443 ", "appId": "app-1"})
	if w.Code != http.StatusOK {
		t.Fatalf("add: %d %s", w.Code, w.Body.String())
	}
	got := decodeOriginAnswer(t, w.Body.String())
	want := []string{testCorsOriginB, testCorsOriginA}
	if !got.Added || !reflect.DeepEqual(got.AllowedOrigins, want) {
		t.Fatalf("answer = %+v", got)
	}
	if f.cors.owner[testCorsProject+"/app-1"] != testCorsOriginA {
		t.Fatalf("the app's origin is not recorded: %v", f.cors.owner)
	}

	w = doJSON(f.router, http.MethodPost, testCorsPath+"/origins", map[string]string{"origin": testCorsOriginA})
	if got := decodeOriginAnswer(t, w.Body.String()); w.Code != http.StatusOK || got.Added {
		t.Fatalf("adding an origin twice changes nothing: %d %+v", w.Code, got)
	}
}

func TestCorsOrigins_AddRefusesWhatIsNotOneOrigin(t *testing.T) {
	f := setupCorsHandler(t)
	for _, body := range []map[string]string{
		{"origin": "*"}, {"origin": "https://a.example.com/path"}, {"origin": ""},
		{"origin": "https://a.example.com,https://b.example.com"}, {"origin": testCorsOriginA, "appId": "../x"},
	} {
		if w := doJSON(f.router, http.MethodPost, testCorsPath+"/origins", body); w.Code != http.StatusBadRequest {
			t.Errorf("%v: %d %s", body, w.Code, w.Body.String())
		}
	}
	if len(f.cors.origins[testCorsProject]) != 0 {
		t.Fatalf("a refused add wrote %v", f.cors.origins[testCorsProject])
	}
}

func TestCorsOrigins_RemoveTakesOneOrigin(t *testing.T) {
	f := setupCorsHandler(t)
	f.cors.origins[testCorsProject] = []string{testCorsOriginB, testCorsOriginA}
	w := doJSON(f.router, http.MethodDelete, testCorsPath+"/origins?origin="+url.QueryEscape("http://LOCALHOST:5173"), nil)
	if w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	got := decodeOriginAnswer(t, w.Body.String())
	if !got.Removed || !reflect.DeepEqual(got.AllowedOrigins, []string{testCorsOriginA}) {
		t.Fatalf("answer = %+v", got)
	}
	w = doJSON(f.router, http.MethodDelete, testCorsPath+"/origins?origin="+url.QueryEscape(testCorsOriginB), nil)
	if got := decodeOriginAnswer(t, w.Body.String()); w.Code != http.StatusOK || got.Removed {
		t.Fatalf("removing an absent origin changes nothing: %d %+v", w.Code, got)
	}
	if w := doJSON(f.router, http.MethodDelete, testCorsPath+"/origins", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("remove without an origin: %d", w.Code)
	}
}

func TestCorsOrigins_UnavailableWithoutAnEditingStore(t *testing.T) {
	f := setupCorsHandler(t)
	f.handler.SetCorsStore(readOnlyCorsStore{})
	if w := doJSON(f.router, http.MethodPost, testCorsPath+"/origins", map[string]string{"origin": testCorsOriginA}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("add without an editor: %d", w.Code)
	}
	f.handler.SetCorsStore(nil)
	if w := doJSON(f.router, http.MethodDelete, testCorsPath+"/origins?origin=x", nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("remove without a store: %d", w.Code)
	}
}

func TestCorsOrigins_StoreRefusalPastTheCapIs400(t *testing.T) {
	f := setupCorsHandler(t)
	full := make([]string, 0, domain.MaxCorsOrigins)
	for i := range domain.MaxCorsOrigins {
		full = append(full, "https://h"+strings.Repeat("x", i+1)+".example.com")
	}
	f.cors.origins[testCorsProject] = full
	if w := doJSON(f.router, http.MethodPost, testCorsPath+"/origins", map[string]string{"origin": testCorsOriginA}); w.Code != http.StatusBadRequest {
		t.Fatalf("past the cap: %d %s", w.Code, w.Body.String())
	}
}

type readOnlyCorsStore struct{}

func (readOnlyCorsStore) GetCorsOrigins(context.Context, string) ([]string, error) {
	return []string{}, nil
}
func (readOnlyCorsStore) SetCorsOrigins(context.Context, string, []string) error { return nil }

func decodeOriginAnswer(t *testing.T, body string) corsOriginAnswer {
	t.Helper()
	var out corsOriginAnswer
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out
}
