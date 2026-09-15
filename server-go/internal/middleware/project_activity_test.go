package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

type recordedActivity struct {
	projectID string
	source    domain.ActivitySource
}

type fakeActivityRecorder struct {
	mu    sync.Mutex
	calls []recordedActivity
}

func (f *fakeActivityRecorder) Record(_ context.Context, projectID string, source domain.ActivitySource) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, recordedActivity{projectID, source})
}

func newActivityRouter(rec ActivityRecorder, status int) *chi.Mux {
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}", func(r chi.Router) {
		r.Use(ProjectActivity(rec))
		r.Get("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
		r.Get("/rls-policies/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
	})
	r.With(ProjectActivity(rec)).Get("/api/tiers", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
	return r
}

func TestProjectActivity_RecordsSuccessfulProjectScopedCall(t *testing.T) {
	rec := &fakeActivityRecorder{}
	r := newActivityRouter(rec, http.StatusOK)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/provision/p1/", nil))

	if len(rec.calls) != 1 || rec.calls[0] != (recordedActivity{"p1", SourceAPI}) {
		t.Fatalf("expected one api activity for p1, got %+v", rec.calls)
	}
}

func TestProjectActivity_ClassifiesDataPlanePolicyFetch(t *testing.T) {
	rec := &fakeActivityRecorder{}
	r := newActivityRouter(rec, http.StatusOK)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/provision/p1/rls-policies/", nil))

	if len(rec.calls) != 1 || rec.calls[0].source != SourcePolicyFetch {
		t.Fatalf("policy fetch must be classified as data-plane activity, got %+v", rec.calls)
	}
}

func TestProjectActivity_SkipsFailedCalls(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusInternalServerError} {
		rec := &fakeActivityRecorder{}
		r := newActivityRouter(rec, status)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/provision/p1/", nil))
		if len(rec.calls) != 0 {
			t.Errorf("status %d must not count as activity, got %+v", status, rec.calls)
		}
	}
}

func TestProjectActivity_SkipsRoutesWithoutProject(t *testing.T) {
	rec := &fakeActivityRecorder{}
	r := newActivityRouter(rec, http.StatusOK)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/tiers", nil))
	if len(rec.calls) != 0 {
		t.Errorf("no projectId → no activity, got %+v", rec.calls)
	}
}

func TestProjectActivity_NilRecorderPassesThrough(t *testing.T) {
	r := newActivityRouter(nil, http.StatusTeapot)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/provision/p1/", nil))
	if w.Code != http.StatusTeapot {
		t.Errorf("nil recorder must be a transparent no-op, got %d", w.Code)
	}
}

func TestActivitySource_ClassifiesByPath(t *testing.T) {
	cases := map[string]domain.ActivitySource{
		"/api/provision/p1/":                      SourceAPI,
		"/api/provision/p1/credentials":           SourceAPI,
		"/api/provision/p1/rls-policies/":         SourcePolicyFetch,
		"/api/provision/p1/column-policies/":      SourcePolicyFetch,
		"/api/provision/p1/backup/manual":         SourceBackup,
		"/api/provision/p1/snapshot":              SourceBackup,
		"/api/provision/p1/migrations":            SourceMigration,
		"/api/projects/p1/schema/apply":           SourceSchema,
		"/api/schema/p1/tables":                   SourceSchema,
		"/api/projects/p1/functions/f1/invoke":    SourceFunctions,
		"/functions/v1/p1/hello":                  SourceFunctionInvoke,
		"/functions/v1/p1/http/anything":          SourceFunctionInvoke,
		"/internal/invoke/p1/f1":                  SourceFunctionInvoke,
		"/api/projects/p1/info/":                  SourceInfo,
		"/api/projects/p1/realtime/subscriptions": SourceRealtime,
		"/api/projects/p1/storage/buckets":        SourceStorage,
	}
	for path, want := range cases {
		if got := ActivitySource(path); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
}
