package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
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
func (f *fakeTierStore) UpsertTierConfig(_ context.Context, tier domain.TierType, tc config.TierConfig, expectedVersion *int) (int, error) {
	current := f.m[tier].Version
	if expectedVersion != nil && *expectedVersion != current {
		return 0, fmt.Errorf("%w: it is at version %d", storage.ErrTierVersionConflict, current)
	}
	tc.Version = current + 1
	f.m[tier] = tc
	return tc.Version, nil
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

// CPU and memory are Kubernetes quantities greater than zero; anything else
// would only fail later, when a cluster is built from the plan.
func TestTierHandler_Update_RejectsUnusableCPUAndMemory(t *testing.T) {
	const valid = `"instances":1,"storageSize":"5Gi","maxStorageSize":"5Gi","maxAppDiskSize":"1Gi","maxApps":2`
	cases := map[string]struct{ body, field string }{
		"cpu not a quantity":    {`{` + valid + `,"memory":"512Mi","cpu":"two"}`, "cpu"},
		"cpu zero":              {`{` + valid + `,"memory":"512Mi","cpu":"0"}`, "cpu"},
		"cpu negative":          {`{` + valid + `,"memory":"512Mi","cpu":"-1"}`, "cpu"},
		"memory not a quantity": {`{` + valid + `,"memory":"lots","cpu":"0.5"}`, "memory"},
		"memory zero":           {`{` + valid + `,"memory":"0Mi","cpu":"0.5"}`, "memory"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
			rec := httptest.NewRecorder()
			NewTierHandler(store).Update(rec, newTierReqWithParam("PUT", tc.body, string(domain.Free)))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.field) {
				t.Errorf("got %d %s, want 400 naming %s", rec.Code, rec.Body.String(), tc.field)
			}
			if len(store.m) != 0 {
				t.Error("a refused spec was stored")
			}
		})
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

// A plan edit re-runs the app disk rule on every app the plan now caps lower.
func TestTierHandler_Update_TellsThePlatformThePlanChanged(t *testing.T) {
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
	h := NewTierHandler(store)
	changed := 0
	h.SetPlanChangeHook(func() { changed++ })
	body := `{"instances":1,"storageSize":"5Gi","maxStorageSize":"5Gi","maxAppDiskSize":"500Mi","maxApps":2,"memory":"512Mi","cpu":"0.5"}`
	rec := httptest.NewRecorder()
	h.Update(rec, newTierReqWithParam("PUT", body, string(domain.Free)))
	if rec.Code != http.StatusOK || changed != 1 {
		t.Fatalf("got %d %s, hook ran %d times; want 200 and one run", rec.Code, rec.Body.String(), changed)
	}
	rec = httptest.NewRecorder()
	h.Update(rec, newTierReqWithParam("PUT", `{"instances":0}`, string(domain.Free)))
	if rec.Code != http.StatusBadRequest || changed != 1 {
		t.Fatalf("a refused edit ran the hook: %d, %d", rec.Code, changed)
	}
}

// siteLimitsBody is a valid plan with one Sites field set to value.
func siteLimitsBody(field, value string) string {
	fields := map[string]string{
		"maxSites": "5", "maxSiteDeployBytes": "104857600", "maxSiteFiles": "20000",
		"maxSiteFileBytes": "26214400", "maxSiteDeploysPerHour": "30", "maxSiteCustomDomains": "2",
	}
	fields[field] = value
	out := `{"instances":1,"storageSize":"5Gi","maxStorageSize":"5Gi","maxAppDiskSize":"1Gi","maxApps":2,"memory":"512Mi","cpu":"0.5"`
	for name, v := range fields {
		out += `,"` + name + `":` + v
	}
	return out + "}"
}

// A plan's Sites limits are bounded (EXC-620): 25 MiB a file is Cloudflare's
// own ceiling.
func TestTierHandler_Update_RefusesSiteLimitsOutOfBounds(t *testing.T) {
	refused := map[string][]string{
		"maxSites":              {"-1", "1001"},
		"maxSiteDeployBytes":    {"0", "1048575", "1073741825"},
		"maxSiteFiles":          {"0", "100001"},
		"maxSiteFileBytes":      {"0", "26214401"},
		"maxSiteDeploysPerHour": {"0", "1001"},
		"maxSiteCustomDomains":  {"-1", "101"},
	}
	for field, values := range refused {
		for _, value := range values {
			t.Run(field+"="+value, func(t *testing.T) {
				store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
				rec := httptest.NewRecorder()
				NewTierHandler(store).Update(rec, newTierReqWithParam("PUT", siteLimitsBody(field, value), string(domain.Free)))
				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), field) || len(store.m) != 0 {
					t.Errorf("got %d %s, stored %d; want 400 naming %s and nothing stored", rec.Code, rec.Body.String(), len(store.m), field)
				}
			})
		}
	}
}

// 0 sites is a plan that offers none; every limit is stored and echoed.
func TestTierHandler_Update_StoresAndEchoesSiteLimits(t *testing.T) {
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
	rec := httptest.NewRecorder()
	NewTierHandler(store).Update(rec, newTierReqWithParam("PUT", siteLimitsBody("maxSites", "0"), string(domain.Free)))
	want := config.SiteTierLimits{MaxSites: 0, MaxDeployBytes: 100 << 20, MaxFiles: 20_000, MaxFileBytes: 25 << 20, MaxDeploysPerHour: 30, MaxCustomDomains: 2}
	if rec.Code != http.StatusOK || store.m[domain.Free].Sites != want {
		t.Fatalf("got %d %s, stored %+v; want the plan's site limits kept", rec.Code, rec.Body.String(), store.m[domain.Free].Sites)
	}
	for _, echoed := range []string{`"maxSites":0`, `"maxSiteDeployBytes":104857600`, `"maxSiteFiles":20000`, `"maxSiteFileBytes":26214400`, `"maxSiteDeploysPerHour":30`, `"maxSiteCustomDomains":2`} {
		if !strings.Contains(rec.Body.String(), echoed) {
			t.Errorf("response %s does not echo %s", rec.Body.String(), echoed)
		}
	}
}

// Sites limits came after every script that sizes a plan, so a body without
// them keeps the plan's current ones instead of being refused or zeroed.
func TestTierHandler_Update_KeepsSiteLimitsLeftOut(t *testing.T) {
	const body = `{"instances":1,"storageSize":"5Gi","maxStorageSize":"5Gi","maxAppDiskSize":"1Gi","maxApps":2,"memory":"512Mi","cpu":"0.25","maxSiteDeploysPerHour":12}`
	stored := config.SiteTierLimits{MaxSites: 7, MaxDeployBytes: 50 << 20, MaxFiles: 900, MaxFileBytes: 5 << 20, MaxDeploysPerHour: 3, MaxCustomDomains: 4}
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{domain.Free: {Sites: stored}}}
	rec := httptest.NewRecorder()
	NewTierHandler(store).Update(rec, newTierReqWithParam("PUT", body, string(domain.Free)))
	want := stored
	want.MaxDeploysPerHour = 12
	if rec.Code != http.StatusOK || store.m[domain.Free].Sites != want {
		t.Fatalf("got %d %s, stored %+v; want %+v", rec.Code, rec.Body.String(), store.m[domain.Free].Sites, want)
	}

	// A plan with no row yet starts from its built-in limits.
	store = &fakeTierStore{m: map[domain.TierType]config.TierConfig{}}
	rec = httptest.NewRecorder()
	NewTierHandler(store).Update(rec, newTierReqWithParam("PUT", body, string(domain.Standard)))
	builtin, _ := config.GetTierConfig(domain.Standard)
	want = builtin.Sites
	want.MaxDeploysPerHour = 12
	if rec.Code != http.StatusOK || store.m[domain.Standard].Sites != want {
		t.Fatalf("got %d %s, stored %+v; want %+v", rec.Code, rec.Body.String(), store.m[domain.Standard].Sites, want)
	}
}

func putTier(h *TierHandler, ifMatch string) *httptest.ResponseRecorder {
	request := newTierReqWithParam("PUT", siteLimitsBody("maxSites", "7"), string(domain.Free))
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	recorder := httptest.NewRecorder()
	h.Update(recorder, request)
	return recorder
}

// Two admins editing one plan: the second save, made from the version before
// the first, is refused instead of silently undoing it (EXC-620).
func TestTierHandler_Update_RefusesAnEditFromAStaleVersion(t *testing.T) {
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{domain.Free: {Version: 3}}}
	h := NewTierHandler(store)
	first := putTier(h, `"3"`)
	var saved tierConfigDTO
	if err := json.Unmarshal(first.Body.Bytes(), &saved); first.Code != http.StatusOK || err != nil || saved.Version != 4 {
		t.Fatalf("first save = %d %s", first.Code, first.Body)
	}
	if etag := first.Header().Get("ETag"); etag != "4" {
		t.Fatalf("ETag = %q, want the new version", etag)
	}
	stale := putTier(h, "3")
	if stale.Code != http.StatusPreconditionFailed || !strings.Contains(stale.Body.String(), "version 4") {
		t.Fatalf("stale save = %d %s, want 412 naming the current version", stale.Code, stale.Body)
	}
	if store.m[domain.Free].Version != 4 {
		t.Fatalf("the stale save was stored: %+v", store.m[domain.Free])
	}
	if bad := putTier(h, "four"); bad.Code != http.StatusBadRequest {
		t.Fatalf("unreadable If-Match = %d", bad.Code)
	}
}

// A script that sizes a plan sends no If-Match and writes unconditionally.
func TestTierHandler_Update_WithoutIfMatchStillWrites(t *testing.T) {
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{domain.Free: {Version: 2}}}
	if response := putTier(NewTierHandler(store), ""); response.Code != http.StatusOK || store.m[domain.Free].Version != 3 {
		t.Fatalf("unconditional save = %d %s", response.Code, response.Body)
	}
}

// The list carries each plan's version, which Studio sends back as If-Match.
func TestTierHandler_List_CarriesEachVersion(t *testing.T) {
	store := &fakeTierStore{m: map[domain.TierType]config.TierConfig{domain.Free: {Version: 5}}}
	recorder := httptest.NewRecorder()
	NewTierHandler(store).List(recorder, httptest.NewRequest("GET", "/", nil))
	var listed []tierConfigDTO
	if err := json.Unmarshal(recorder.Body.Bytes(), &listed); err != nil || len(listed) != 1 || listed[0].Version != 5 {
		t.Fatalf("listed = %s", recorder.Body)
	}
}
