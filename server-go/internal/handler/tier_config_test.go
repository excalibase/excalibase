package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

type fakeTierStore struct {
	m map[domain.TierType]config.TierConfig
}

func (f *fakeTierStore) ListTierConfigs(_ context.Context) (map[domain.TierType]config.TierConfig, error) {
	return f.m, nil
}
func (f *fakeTierStore) GetTierConfig(_ context.Context, tier domain.TierType) (config.TierConfig, bool, error) {
	tc, ok := f.m[tier]
	return tc, ok, nil
}
func (f *fakeTierStore) UpsertTierConfig(_ context.Context, tier domain.TierType, tc config.TierConfig) error {
	f.m[tier] = tc
	return nil
}

func newTierReqWithParam(method, body, tier string) *http.Request {
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("tier", tier)
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

func TestTierHandler_List(t *testing.T) {
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{
		domain.Free: {Instances: 1, CPU: "0.5", Memory: "512Mi", StorageSize: "5Gi"},
	}}
	h := NewTierHandler(store)

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d", rec.Code)
	}
	var out []tierConfigDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 || out[0].Tier != domain.Free || out[0].CPU != "0.5" {
		t.Errorf("unexpected list payload: %+v", out)
	}
}

func TestTierHandler_Update_PersistsAndEchoes(t *testing.T) {
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
	h := NewTierHandler(store)

	body := `{"maxProjects":5,"instances":1,"storageSize":"50Gi","memory":"4Gi","cpu":"2","backupEnabled":true}`
	rec := httptest.NewRecorder()
	h.Update(rec, newTierReqWithParam("PUT", body, string(domain.Standard)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", rec.Code, rec.Body.String())
	}
	got := store.m[domain.Standard]
	if got.CPU != "2" || got.Memory != "4Gi" || got.Instances != 1 || !got.BackupEnabled {
		t.Errorf("store not updated correctly: %+v", got)
	}
}

func TestTierHandler_Update_UnknownTier(t *testing.T) {
	h := NewTierHandler(&fakeTierStore{m: map[domain.TierType]config.TierConfig{}})
	rec := httptest.NewRecorder()
	h.Update(rec, newTierReqWithParam("PUT", `{"cpu":"2","memory":"4Gi","storageSize":"5Gi","instances":1}`, "PLATINUM"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for unknown tier, got %d", rec.Code)
	}
}

func TestTierHandler_Update_RejectsInvalidSpec(t *testing.T) {
	h := NewTierHandler(&fakeTierStore{m: map[domain.TierType]config.TierConfig{}})
	rec := httptest.NewRecorder()
	// instances 0 + missing quantities → invalid
	h.Update(rec, newTierReqWithParam("PUT", `{"instances":0}`, string(domain.Free)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for invalid spec, got %d", rec.Code)
	}
}

func TestTierHandler_Routes_ListServed(t *testing.T) {
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{
		domain.Free: {Instances: 1, CPU: "0.5", Memory: "512Mi", StorageSize: "5Gi"},
	}}
	h := NewTierHandler(store)
	r := chi.NewRouter()
	r.Route("/api/admin/tiers", h.Routes) // exercises Routes wiring

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/api/admin/tiers/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET via Routes should 200, got %d", rec.Code)
	}
}
