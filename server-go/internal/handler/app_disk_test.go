package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

type fakeDiskLimits struct {
	maxGiB int
	err    error
}

func (f fakeDiskLimits) MaxDiskGiB(context.Context, string) (int, error) { return f.maxGiB, f.err }

func setupDiskAppRouter(t *testing.T, limits apphost.DiskLimits) (chi.Router, *fakeAppStore) {
	t.Helper()
	store := newFakeAppStore()
	h := NewAppHandler(store, newFakeSources("storefront_db"), testAppRoute)
	h.SetAppLimits(fakeAppLimits{limit: testAppLimit})
	if limits != nil {
		h.SetDiskLimits(limits)
	}
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/apps", func(r chi.Router) { h.Routes(r) })
	return r, store
}

func diskAppBody(size string) map[string]any {
	body := validAppBody()
	body["disk"] = map[string]any{"mountPath": "/data", "size": size}
	return body
}

func TestAppCreateWithADiskWithinThePlan(t *testing.T) {
	r, store := setupDiskAppRouter(t, fakeDiskLimits{maxGiB: 20})
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", diskAppBody("20Gi"))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	app := decodeApp(t, w)
	if app.Disk == nil || app.Disk.Size != "20Gi" || app.Disk.MountPath != "/data" {
		t.Fatalf("disk = %+v", app.Disk)
	}
	if stored := store.apps[appKey(appTestProject, app.ID)]; stored.Disk == nil {
		t.Fatal("the disk must reach the store")
	}
}

func TestAppCreateRefusesADiskAboveThePlan(t *testing.T) {
	r, store := setupDiskAppRouter(t, fakeDiskLimits{maxGiB: 20})
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", diskAppBody("21Gi"))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "20Gi") {
		t.Fatalf("got %d %s, want 409 naming the plan's cap", w.Code, w.Body.String())
	}
	if len(store.apps) != 0 {
		t.Fatal("a refused app was stored")
	}
}

func TestAppCreateRefusesADiskItCannotCheck(t *testing.T) {
	for name, limits := range map[string]apphost.DiskLimits{
		"no limits configured": nil,
		"the plan unreadable":  fakeDiskLimits{err: fmt.Errorf("%w: tier store down", service.ErrOrgTierUnresolved)},
	} {
		t.Run(name, func(t *testing.T) {
			r, store := setupDiskAppRouter(t, limits)
			w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", diskAppBody("1Gi"))
			if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "tier store") {
				t.Fatalf("got %d %s, want a plain 500", w.Code, w.Body.String())
			}
			if len(store.apps) != 0 {
				t.Fatal("an unchecked disk was stored")
			}
		})
	}
}

func TestAppCreateRefusesABadDisk(t *testing.T) {
	r, _ := setupDiskAppRouter(t, fakeDiskLimits{maxGiB: 20})
	for name, mutate := range map[string]func(map[string]any){
		"system directory": func(b map[string]any) { b["disk"] = map[string]any{"mountPath": "/etc", "size": "1Gi"} },
		"fractional size":  func(b map[string]any) { b["disk"] = map[string]any{"mountPath": "/data", "size": "1.5Gi"} },
		"two copies":       func(b map[string]any) { b["replicas"] = 2 },
	} {
		body := diskAppBody("1Gi")
		mutate(body)
		if w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d %s, want 400", name, w.Code, w.Body.String())
		}
	}
}

func TestAppUpdateAttachesADiskWithinThePlan(t *testing.T) {
	r, _ := setupDiskAppRouter(t, fakeDiskLimits{maxGiB: 5})
	app := createAppForTest(t, r)
	path := "/api/projects/" + appTestProject + "/apps/" + app.ID + "/"

	over := doAppRequest(t, r, http.MethodPatch, path, map[string]any{"disk": map[string]any{"mountPath": "/data", "size": "6Gi"}})
	if over.Code != http.StatusConflict {
		t.Fatalf("over the plan: got %d %s, want 409", over.Code, over.Body.String())
	}
	w := doAppRequest(t, r, http.MethodPatch, path, map[string]any{"disk": map[string]any{"mountPath": "/data", "size": "5Gi"}})
	if w.Code != http.StatusOK {
		t.Fatalf("attach: %d %s", w.Code, w.Body.String())
	}
	if got := decodeApp(t, w); got.Disk == nil || got.Disk.Size != "5Gi" {
		t.Fatalf("disk = %+v", got.Disk)
	}
}

// Once attached, only the mount path changes through an edit: the size grows
// through its own route, and there is no detaching short of deleting the app.
func TestAppUpdateOfAnAttachedDisk(t *testing.T) {
	r, _ := setupDiskAppRouter(t, fakeDiskLimits{maxGiB: 20})
	w := doAppRequest(t, r, http.MethodPost, "/api/projects/"+appTestProject+"/apps/", diskAppBody("5Gi"))
	app := decodeApp(t, w)
	path := "/api/projects/" + appTestProject + "/apps/" + app.ID + "/"

	resized := doAppRequest(t, r, http.MethodPatch, path, map[string]any{"disk": map[string]any{"mountPath": "/data", "size": "8Gi"}})
	if resized.Code != http.StatusBadRequest || !strings.Contains(resized.Body.String(), "/disk") {
		t.Fatalf("size through an edit: got %d %s, want 400 pointing at the disk route", resized.Code, resized.Body.String())
	}
	moved := doAppRequest(t, r, http.MethodPatch, path, map[string]any{"disk": map[string]any{"mountPath": "/var/lib/data", "size": "5Gi"}})
	if moved.Code != http.StatusOK || decodeApp(t, moved).Disk.MountPath != "/var/lib/data" {
		t.Fatalf("move the mount: %d %s", moved.Code, moved.Body.String())
	}
	kept := doAppRequestWithVersion(t, r, http.MethodPatch, path, map[string]any{"disk": nil, "port": 9090}, 2)
	if kept.Code != http.StatusOK || decodeApp(t, kept).Disk == nil {
		t.Fatalf("an edit that does not name the disk keeps it: %d %s", kept.Code, kept.Body.String())
	}
}

// --- lifecycle routes ---

type diskDeployer struct {
	*fakeAppDeployer
	confirmed []bool
	grown     []string
	growErr   error
	deleteErr error
}

func (d *diskDeployer) DeleteApp(_ context.Context, projectID, appID string, confirmDeleteDisk bool) error {
	d.confirmed = append(d.confirmed, confirmDeleteDisk)
	return d.deleteErr
}

func (d *diskDeployer) GrowAppDisk(_ context.Context, projectID, appID, size string) (*apphost.App, error) {
	d.grown = append(d.grown, size)
	if d.growErr != nil {
		return nil, d.growErr
	}
	return &apphost.App{ID: appID, ProjectID: projectID, Disk: &apphost.AppDisk{MountPath: "/data", Size: size}}, nil
}

func diskLifecycleRouter(deployer AppDeployer) chi.Router {
	h := NewAppDeployHandler(deployer)
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/apps/{appId}", func(r chi.Router) {
		r.Delete("/", h.Delete)
		r.Post("/disk", h.GrowDisk)
	})
	return r
}

func doBodyRequest(r chi.Router, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(auth.SetUser(req.Context(), &domain.User{ID: "dev-1", Active: true}))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const diskAppPath = "/api/projects/" + deployHandlerProject + "/apps/app-1/"

func TestAppDelete_ReadsTheDiskConfirmation(t *testing.T) {
	deployer := &diskDeployer{fakeAppDeployer: newFakeAppDeployer()}
	r := diskLifecycleRouter(deployer)
	for _, body := range []string{"", `{}`, `{"confirmDeleteDisk":false}`, `{"confirmDeleteDisk":true}`} {
		if w := doBodyRequest(r, http.MethodDelete, diskAppPath, body); w.Code != http.StatusNoContent {
			t.Fatalf("%q: got %d %s", body, w.Code, w.Body.String())
		}
	}
	want := []bool{false, false, false, true}
	if fmt.Sprint(deployer.confirmed) != fmt.Sprint(want) {
		t.Fatalf("confirmations = %v, want %v", deployer.confirmed, want)
	}
	if w := doBodyRequest(r, http.MethodDelete, diskAppPath, `{"confirmDeleteDisk":`); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: got %d, want 400", w.Code)
	}
	deployer.deleteErr = service.ErrAppDiskDeleteUnconfirmed
	w := doBodyRequest(r, http.MethodDelete, diskAppPath, "")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "confirmDeleteDisk") {
		t.Fatalf("unconfirmed: got %d %s, want 409 naming confirmDeleteDisk", w.Code, w.Body.String())
	}
}

func TestAppGrowDisk(t *testing.T) {
	deployer := &diskDeployer{fakeAppDeployer: newFakeAppDeployer()}
	r := diskLifecycleRouter(deployer)
	w := doBodyRequest(r, http.MethodPost, diskAppPath+"disk", `{"size":"8Gi"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"size":"8Gi"`) {
		t.Fatalf("grow: %d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{``, `{}`, `{"size":`, `{"size":8}`} {
		if w := doBodyRequest(r, http.MethodPost, diskAppPath+"disk", body); w.Code != http.StatusBadRequest {
			t.Errorf("%q: got %d, want 400", body, w.Code)
		}
	}
	cases := map[error]int{
		fmt.Errorf("%w: bad", apphost.ErrInvalidDisk):              http.StatusBadRequest,
		fmt.Errorf("%w: 5Gi", service.ErrAppDiskShrink):            http.StatusBadRequest,
		fmt.Errorf("%w: up to 20Gi", apphost.ErrDiskAbovePlan):     http.StatusConflict,
		service.ErrAppHasNoDisk:                                    http.StatusConflict,
		fmt.Errorf("%w: local-path", k8s.ErrAppDiskNotExpandable):  http.StatusConflict,
		service.ErrProjectOperationRunning:                         http.StatusConflict,
		apphost.ErrAppNotFound:                                     http.StatusNotFound,
		fmt.Errorf("%w: store down", service.ErrOrgTierUnresolved): http.StatusInternalServerError,
		errors.New("pq: connection refused 10.0.0.1"):              http.StatusInternalServerError,
	}
	for err, want := range cases {
		deployer.growErr = err
		w := doBodyRequest(r, http.MethodPost, diskAppPath+"disk", `{"size":"8Gi"}`)
		if w.Code != want || strings.Contains(w.Body.String(), "10.0.0.1") {
			t.Errorf("%v: got %d %s, want %d", err, w.Code, w.Body.String(), want)
		}
	}
}
