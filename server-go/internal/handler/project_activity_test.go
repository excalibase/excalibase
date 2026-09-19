package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// activityStoreForHandler is an in-memory storage.ProjectActivityStore that
// counts writes so the handler tests can prove the throttle window holds
// end-to-end (middleware → recorder → store).
type activityStoreForHandler struct {
	mu     sync.Mutex
	rows   map[string]domain.ProjectActivity
	writes int
}

func newActivityStoreForHandler() *activityStoreForHandler {
	return &activityStoreForHandler{rows: map[string]domain.ProjectActivity{}}
}

func (s *activityStoreForHandler) TouchProjectActivity(_ context.Context, projectID, source string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	s.rows[projectID] = domain.ProjectActivity{ProjectID: projectID, LastSeenAt: at, LastSeenSource: source}
	return nil
}

func (s *activityStoreForHandler) MarkIdleWarned(_ context.Context, projectID string, lastSeen, warnedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[projectID]
	if !ok {
		row = domain.ProjectActivity{ProjectID: projectID, LastSeenAt: lastSeen, LastSeenSource: "created"}
	}
	row.IdleWarnedAt = &warnedAt
	s.rows[projectID] = row
	return nil
}

func (s *activityStoreForHandler) GetProjectActivity(_ context.Context, projectID string) (domain.ProjectActivity, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.rows[projectID]
	return row, ok, nil
}

func (s *activityStoreForHandler) ListProjectActivity(_ context.Context) (map[string]domain.ProjectActivity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]domain.ProjectActivity, len(s.rows))
	for k, v := range s.rows {
		out[k] = v
	}
	return out, nil
}

func (s *activityStoreForHandler) writeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writes
}

// setupActivityRouter mirrors main.go's /api/provision mount: TenantContext,
// then the activity middleware, then the provisioning handler.
func setupActivityRouter(t *testing.T) (*chi.Mux, *storage.FileSystemStore, *activityStoreForHandler) {
	t.Helper()
	store, _ := storage.NewFileSystemStore(t.TempDir())
	provSvc := service.NewProvisioningService(store, provisioner.NewFactory(), k8s.NewMockClient())
	activity := newActivityStoreForHandler()
	recorder := service.NewActivityRecorder(service.ActivityRecorderConfig{Store: activity, Window: 5 * time.Minute})

	h := NewProvisioningHandler(provSvc, &adminOrgStore{})
	h.SetActivityStore(activity)

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.SetUser(req.Context(), &domain.User{ID: "admin", Role: "platform_admin", Active: true})
			next.ServeHTTP(w, req.WithContext(ctx))
		})
	})
	r.Route("/api/provision", func(r chi.Router) {
		r.Get("/", h.ListInstances)
		r.Route("/{projectId}", func(r chi.Router) {
			r.Use(custommw.TenantContext)
			r.Use(custommw.ProjectActivity(recorder))
			r.Get("/", h.GetStatus)
		})
	})
	return r, store, activity
}

func getJSON(t *testing.T, r http.Handler, path string) (int, []byte) {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Code, w.Body.Bytes()
}

func TestProjectActivity_ProjectScopedCallBumpsLastSeenOncePerWindow(t *testing.T) {
	r, store, activity := setupActivityRouter(t)
	_ = store.Create(&domain.DatabaseInstance{ProjectID: "p1", OrgID: "o", Status: "ACTIVE"})

	for i := 0; i < 3; i++ {
		if code, body := getJSON(t, r, "/api/provision/p1/"); code != http.StatusOK {
			t.Fatalf("GET status: %d body=%s", code, body)
		}
	}
	if got := activity.writeCount(); got != 1 {
		t.Fatalf("three calls inside one window must produce one write, got %d", got)
	}
	row, ok, _ := activity.GetProjectActivity(context.Background(), "p1")
	if !ok || row.LastSeenSource != custommw.SourceAPI.String() {
		t.Errorf("activity row: ok=%v row=%+v", ok, row)
	}
}

func TestProjectActivity_MissingProjectDoesNotBump(t *testing.T) {
	r, _, activity := setupActivityRouter(t)
	if code, _ := getJSON(t, r, "/api/provision/ghost/"); code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", code)
	}
	if activity.writeCount() != 0 {
		t.Error("a 404 must not record activity")
	}
}

func TestProjectActivity_LastSeenAtExposedOnGetAndList(t *testing.T) {
	r, store, activity := setupActivityRouter(t)
	_ = store.Create(&domain.DatabaseInstance{ProjectID: "p1", OrgID: "o", Status: "ACTIVE"})
	_ = store.Create(&domain.DatabaseInstance{ProjectID: "p2", OrgID: "o", Status: "ACTIVE"})
	seen := time.Date(2026, 9, 10, 8, 30, 0, 0, time.UTC)
	_ = activity.TouchProjectActivity(context.Background(), "p1", custommw.SourceFunctions.String(), seen)

	_, body := getJSON(t, r, "/api/provision/p1/")
	var single map[string]any
	if err := json.Unmarshal(body, &single); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	if single["projectId"] != "p1" || single["lastSeenAt"] != "2026-09-10T08:30:00.000000000" {
		t.Errorf("GET must carry projectId + lastSeenAt: %v", single)
	}

	_, body = getJSON(t, r, "/api/provision/")
	var list []map[string]any
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	byID := map[string]map[string]any{}
	for _, item := range list {
		byID[item["projectId"].(string)] = item
	}
	// The GET above was itself activity, so p1's marker has moved past the seed.
	if seenAt, _ := byID["p1"]["lastSeenAt"].(string); seenAt <= "2026-09-10T08:30:00.000000000" {
		t.Errorf("list must carry the bumped lastSeenAt for p1: %v", byID["p1"])
	}
	if _, present := byID["p2"]["lastSeenAt"]; present {
		t.Errorf("p2 has no activity row so lastSeenAt must be omitted: %v", byID["p2"])
	}
}

func TestProjectActivity_NoStoreLeavesResponsesUnchanged(t *testing.T) {
	store, _ := storage.NewFileSystemStore(t.TempDir())
	provSvc := service.NewProvisioningService(store, provisioner.NewFactory(), k8s.NewMockClient())
	h := NewProvisioningHandler(provSvc, nil)
	_ = store.Create(&domain.DatabaseInstance{ProjectID: "p1", OrgID: "o", Status: "ACTIVE"})

	r := chi.NewRouter()
	r.Get("/api/provision/{projectId}", h.GetStatus)
	code, body := getJSON(t, r, "/api/provision/p1")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	if _, present := got["lastSeenAt"]; present || got["projectId"] != "p1" {
		t.Errorf("without an activity store the payload must be the bare instance: %v", got)
	}
}
