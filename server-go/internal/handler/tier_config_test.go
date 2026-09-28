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

	body := `{"maxProjects":5,"instances":1,"storageSize":"50Gi","maxStorageSize":"500Gi","maxAppDiskSize":"20Gi","maxApps":5,"memory":"4Gi","cpu":"2","backupEnabled":true}`
	rec := httptest.NewRecorder()
	h.Update(rec, newTierReqWithParam("PUT", body, string(domain.Standard)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", rec.Code, rec.Body.String())
	}
	got := store.m[domain.Standard]
	if got.CPU != "2" || got.Memory != "4Gi" || got.Instances != 1 || !got.BackupEnabled || got.MaxStorageSize != "500Gi" || got.MaxAppDiskSize != "20Gi" {
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

func TestTierHandler_Update_AutoPauseAfterDaysRoundTrips(t *testing.T) {
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
	h := NewTierHandler(store)

	body := `{"maxProjects":1,"instances":1,"storageSize":"5Gi","maxStorageSize":"5Gi","maxAppDiskSize":"1Gi","maxApps":2,"memory":"512Mi","cpu":"0.5","autoPauseAfterDays":3}`
	rec := httptest.NewRecorder()
	h.Update(rec, newTierReqWithParam("PUT", body, string(domain.Free)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := store.m[domain.Free].AutoPauseAfterDays; got != 3 {
		t.Errorf("autoPauseAfterDays not persisted: %d", got)
	}
	var echoed tierConfigDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &echoed)
	if echoed.AutoPauseAfterDays != 3 {
		t.Errorf("autoPauseAfterDays not echoed: %+v", echoed)
	}
}

func TestTierHandler_Update_RejectsNegativeAutoPause(t *testing.T) {
	h := NewTierHandler(&fakeTierStore{m: map[domain.TierType]config.TierConfig{}})
	rec := httptest.NewRecorder()
	body := `{"instances":1,"storageSize":"5Gi","memory":"512Mi","cpu":"0.5","autoPauseAfterDays":-1}`
	h.Update(rec, newTierReqWithParam("PUT", body, string(domain.Free)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for negative autoPauseAfterDays, got %d", rec.Code)
	}
}

func TestTierHandler_Update_RejectsUnusableStorageSize(t *testing.T) {
	h := NewTierHandler(&fakeTierStore{m: map[domain.TierType]config.TierConfig{}})
	rec := httptest.NewRecorder()
	body := `{"instances":1,"storageSize":"lots","memory":"512Mi","cpu":"0.5"}`
	h.Update(rec, newTierReqWithParam("PUT", body, string(domain.Free)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for an unparseable storageSize, got %d", rec.Code)
	}
}

// A plan's maximum disk is what a project may grow to (EXC-492): required, a
// storage quantity, and never below the disk the plan starts with.
func TestTierHandler_Update_MaxStorageSize(t *testing.T) {
	cases := map[string]string{
		"missing":         `{"instances":1,"storageSize":"5Gi","memory":"512Mi","cpu":"0.5"}`,
		"not a quantity":  `{"instances":1,"storageSize":"5Gi","maxStorageSize":"lots","memory":"512Mi","cpu":"0.5"}`,
		"below the start": `{"instances":1,"storageSize":"50Gi","maxStorageSize":"10Gi","memory":"512Mi","cpu":"0.5"}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
			rec := httptest.NewRecorder()
			NewTierHandler(store).Update(rec, newTierReqWithParam("PUT", body, string(domain.Free)))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "maxStorageSize") {
				t.Errorf("got %d %s, want 400 naming maxStorageSize", rec.Code, rec.Body.String())
			}
			if len(store.m) != 0 {
				t.Error("a refused spec was stored")
			}
		})
	}
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
	rec := httptest.NewRecorder()
	NewTierHandler(store).Update(rec, newTierReqWithParam("PUT", `{"instances":1,"storageSize":"5Gi","maxStorageSize":"5Gi","maxAppDiskSize":"1Gi","maxApps":2,"memory":"512Mi","cpu":"0.5"}`, string(domain.Free)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"maxStorageSize":"5Gi"`) {
		t.Errorf("a fixed disk (max = start) was refused: %d %s", rec.Code, rec.Body.String())
	}
}

// A plan's app-disk cap is the largest disk one app may have (EXC-523):
// required, whole gibibytes, and 0Gi when the plan offers no app disks.
func TestTierHandler_Update_MaxAppDiskSize(t *testing.T) {
	base := `"instances":1,"storageSize":"5Gi","maxStorageSize":"5Gi","maxApps":2,"memory":"512Mi","cpu":"0.5"`
	for name, field := range map[string]string{
		"missing":             ``,
		"not whole gibibytes": `,"maxAppDiskSize":"1.5Gi"`,
		"not a quantity":      `,"maxAppDiskSize":"lots"`,
		"another unit":        `,"maxAppDiskSize":"1Ti"`,
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
			rec := httptest.NewRecorder()
			NewTierHandler(store).Update(rec, newTierReqWithParam("PUT", "{"+base+field+"}", string(domain.Free)))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "maxAppDiskSize") {
				t.Errorf("got %d %s, want 400 naming maxAppDiskSize", rec.Code, rec.Body.String())
			}
			if len(store.m) != 0 {
				t.Error("a refused spec was stored")
			}
		})
	}
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
	rec := httptest.NewRecorder()
	NewTierHandler(store).Update(rec, newTierReqWithParam("PUT", "{"+base+`,"maxAppDiskSize":"0Gi"}`, string(domain.Free)))
	if rec.Code != http.StatusOK || store.m[domain.Free].MaxAppDiskSize != "0Gi" || !strings.Contains(rec.Body.String(), `"maxAppDiskSize":"0Gi"`) {
		t.Errorf("a plan with no app disks was refused: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTierHandler_Update_MaxApps(t *testing.T) {
	base := `"instances":1,"storageSize":"5Gi","maxStorageSize":"5Gi","maxAppDiskSize":"1Gi","memory":"512Mi","cpu":"0.5"`
	for name, field := range map[string]string{
		"missing":  ``,
		"negative": `,"maxApps":-1`,
		"too many": `,"maxApps":1001`,
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
			rec := httptest.NewRecorder()
			NewTierHandler(store).Update(rec, newTierReqWithParam("PUT", "{"+base+field+"}", string(domain.Free)))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "maxApps") {
				t.Errorf("got %d %s, want 400 naming maxApps", rec.Code, rec.Body.String())
			}
			if len(store.m) != 0 {
				t.Error("a refused spec was stored")
			}
		})
	}
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
	rec := httptest.NewRecorder()
	NewTierHandler(store).Update(rec, newTierReqWithParam("PUT", "{"+base+`,"maxApps":0}`, string(domain.Free)))
	if rec.Code != http.StatusOK || store.m[domain.Free].MaxApps != 0 || !strings.Contains(rec.Body.String(), `"maxApps":0`) {
		t.Errorf("a plan with no apps was refused: %d %s", rec.Code, rec.Body.String())
	}
}
