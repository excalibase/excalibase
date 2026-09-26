package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/go-chi/chi/v5"
)

// fakeGrantStore is an in-memory storage.TableGrantStore. It keeps the
// handler tests free of Docker while still exercising the real routing,
// validation, serialization and event-publishing paths.
type fakeGrantStore struct {
	mu       sync.Mutex
	grants   map[string]domain.TableGrant // keyed by id
	failWith error
}

func newFakeGrantStore() *fakeGrantStore {
	return &fakeGrantStore{grants: map[string]domain.TableGrant{}}
}

func (f *fakeGrantStore) ListGrants(_ context.Context, projectID string) ([]domain.TableGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	out := []domain.TableGrant{}
	for _, g := range f.grants {
		if g.ProjectID == projectID {
			out = append(out, g)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Resource == out[j].Resource {
			return out[i].Role < out[j].Role
		}
		return out[i].Resource < out[j].Resource
	})
	return out, nil
}

func (f *fakeGrantStore) GetGrant(_ context.Context, projectID, id string) (*domain.TableGrant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.grants[id]
	if !ok || g.ProjectID != projectID {
		return nil, pgstore.ErrGrantNotFound
	}
	copied := g
	return &copied, nil
}

func (f *fakeGrantStore) UpsertGrant(_ context.Context, g *domain.TableGrant) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	if existing, ok := f.grants[g.ID]; ok && existing.ProjectID != g.ProjectID {
		return errors.New("owned by another project")
	}
	f.grants[g.ID] = *g
	return nil
}

func (f *fakeGrantStore) DeleteGrant(_ context.Context, projectID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g, ok := f.grants[id]
	if !ok || g.ProjectID != projectID {
		return pgstore.ErrGrantNotFound
	}
	delete(f.grants, id)
	return nil
}

// everyProject treats any project id as known; the unknown-project answer is
// covered in policy_project_lookup_test.go.
type everyProject struct{}

func (everyProject) FindByProjectID(projectID string) (*domain.DatabaseInstance, error) {
	return &domain.DatabaseInstance{ProjectID: projectID}, nil
}

// grantBus records published change events without dialing NATS.
type grantBus struct {
	mu     sync.Mutex
	events []domain.PolicyChangeEvent
}

func (b *grantBus) PublishPolicyChange(_ context.Context, e domain.PolicyChangeEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
}

func (b *grantBus) snapshot() []domain.PolicyChangeEvent {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]domain.PolicyChangeEvent, len(b.events))
	copy(out, b.events)
	return out
}

func setupGrantRouter(t *testing.T) (chi.Router, *fakeGrantStore, *grantBus) {
	t.Helper()
	return setupGrantRouterEnforcing(t, true)
}

// setupGrantRouterEnforcing builds the router with the platform-wide exposure
// kill switch in a chosen position. Nothing in the HTTP surface can change it.
func setupGrantRouterEnforcing(t *testing.T, enforced bool) (chi.Router, *fakeGrantStore, *grantBus) {
	t.Helper()
	store := newFakeGrantStore()
	bus := &grantBus{}
	h := NewTableGrantHandler(store, everyProject{}, enforced)
	h.SetPublisher(bus)

	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}/table-grants", func(r chi.Router) { h.Routes(r) })
	return r, store, bus
}

func grantJSONBody(t *testing.T, v any) *bytes.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return bytes.NewReader(b)
}

func validGrant() map[string]any {
	return map[string]any{
		"resource":   "public.orders",
		"operations": []string{"SELECT"},
		"role":       "authenticated",
		"enabled":    true,
	}
}

func doGrantRequest(t *testing.T, r chi.Router, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, grantJSONBody(t, body))
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeGrantSet(t *testing.T, w *httptest.ResponseRecorder) domain.TableGrantSet {
	t.Helper()
	var set domain.TableGrantSet
	if err := json.Unmarshal(w.Body.Bytes(), &set); err != nil {
		t.Fatalf("decode grant set: %v (body=%s)", err, w.Body.String())
	}
	return set
}

// EXC-400: enforcement is ON for every project. There is no per-project
// opt-in and no row whose absence means "serve everything" — a project the
// operator has never touched is served enforced with zero grants, which means
// deny everything. The flag stays on the wire and is never inferred from the
// grant list, so the engine reads a decision rather than guessing at one.
func TestTableGrants_EveryProjectIsEnforcedWithoutOptIn(t *testing.T) {
	r, _, _ := setupGrantRouter(t)

	w := doGrantRequest(t, r, "GET", "/api/provision/proj-untouched/table-grants/", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	set := decodeGrantSet(t, w)
	if !set.Enforced {
		t.Error("a project with no exposure configuration must still be enforced")
	}
	if len(set.Grants) != 0 {
		t.Errorf("expected zero grants, got %+v", set.Grants)
	}
	if !strings.Contains(w.Body.String(), `"enforced":true`) {
		t.Errorf("response must carry the enforced flag explicitly: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"grants":null`) {
		t.Errorf("grants must serialize as [], never null: %s", w.Body.String())
	}
}

// The one way to turn exposure off is the platform-wide setting, and it turns
// it off for every project at once.
func TestTableGrants_PlatformKillSwitchOffReportsNotEnforced(t *testing.T) {
	r, _, _ := setupGrantRouterEnforcing(t, false)

	w := doGrantRequest(t, r, "GET", "/api/provision/proj-a/table-grants/", nil)
	set := decodeGrantSet(t, w)
	if set.Enforced {
		t.Error("with the platform kill switch off, no project may report enforced=true")
	}
	if !strings.Contains(w.Body.String(), `"enforced":false`) {
		t.Errorf("response must carry the enforced flag explicitly: %s", w.Body.String())
	}
}

// The kill switch is platform-wide and belongs to the operator's environment.
// No HTTP route may reach it — a route would put the whole installation's
// exposure filter behind one authenticated call.
func TestTableGrants_NoHTTPRouteCanSetEnforcement(t *testing.T) {
	r, _, bus := setupGrantRouter(t)

	for _, method := range []string{"PUT", "POST", "PATCH", "DELETE"} {
		w := doGrantRequest(t, r, method, "/api/provision/proj-a/table-grants/enforcement",
			map[string]any{"enforced": false})
		if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /enforcement = %d, want the route to be gone: %s",
				method, w.Code, w.Body.String())
		}
	}
	if len(bus.snapshot()) != 0 {
		t.Error("a refused enforcement call must publish nothing")
	}
	// And after all that, enforcement is exactly where it started.
	if !decodeGrantSet(t, doGrantRequest(t, r, "GET", "/api/provision/proj-a/table-grants/", nil)).Enforced {
		t.Fatal("enforcement was reachable over HTTP")
	}
}

// Grants name end users: anon (not signed in) and authenticated (signed in).
// Any other role is refused with a message that says where it belongs —
// arbitrary roles are an RLS policy concern, not an exposure one.
func TestTableGrants_Create_RefusesAnyRoleButAnonAndAuthenticated(t *testing.T) {
	r, store, bus := setupGrantRouter(t)

	for _, role := range []string{"user", "*", "admin", "service_role", "Anon ", "", "postgres"} {
		body := validGrant()
		body["role"] = role
		w := doGrantRequest(t, r, "POST", "/api/provision/proj-a/table-grants/", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("role %q = %d, want 400: %s", role, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), "anon") || !strings.Contains(w.Body.String(), "authenticated") {
			t.Errorf("role %q: message must name the two accepted roles, got %s", role, w.Body.String())
		}
	}
	if len(store.grants) != 0 {
		t.Errorf("a refused grant must not be stored: %+v", store.grants)
	}
	if len(bus.snapshot()) != 0 {
		t.Error("a refused grant must publish nothing")
	}
}

func TestTableGrants_Create_AcceptsTheTwoEndUserRoles(t *testing.T) {
	r, _, _ := setupGrantRouter(t)

	for _, role := range []string{domain.GrantRoleAnon, domain.GrantRoleAuthenticated} {
		body := validGrant()
		body["role"] = role
		if w := doGrantRequest(t, r, "POST", "/api/provision/proj-a/table-grants/", body); w.Code != http.StatusCreated {
			t.Errorf("role %q = %d, want 201: %s", role, w.Code, w.Body.String())
		}
	}
}

// A PATCH must not be a way round the role rule the POST enforces.
func TestTableGrants_Update_RefusesARoleOutsideTheEndUserRoles(t *testing.T) {
	r, store, _ := setupGrantRouter(t)

	created := doGrantRequest(t, r, "POST", "/api/provision/proj-a/table-grants/", validGrant())
	var grant domain.TableGrant
	if err := json.Unmarshal(created.Body.Bytes(), &grant); err != nil {
		t.Fatalf("decode created grant: %v", err)
	}

	w := doGrantRequest(t, r, "PATCH", "/api/provision/proj-a/table-grants/"+grant.ID,
		map[string]any{"role": "service_role"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("patching the role to service_role = %d, want 400: %s", w.Code, w.Body.String())
	}
	if store.grants[grant.ID].Role != domain.GrantRoleAuthenticated {
		t.Fatalf("stored role changed to %q despite the refusal", store.grants[grant.ID].Role)
	}
}

func TestTableGrants_Create_ReturnsCreatedAndEmitsEvent(t *testing.T) {
	r, _, bus := setupGrantRouter(t)

	w := doGrantRequest(t, r, "POST", "/api/provision/proj-a/table-grants/", validGrant())
	if w.Code != http.StatusCreated {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}

	var got domain.TableGrant
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID == "" {
		t.Error("server must assign an id")
	}
	if got.ProjectID != "proj-a" {
		t.Errorf("projectId must come from the path, got %q", got.ProjectID)
	}

	events := bus.snapshot()
	if len(events) != 1 {
		t.Fatalf("want one change event, got %+v", events)
	}
	if events[0].ProjectID != "proj-a" || events[0].Kind != domain.GrantChangeKind ||
		events[0].Op != "create" || events[0].Resource != "public.orders" {
		t.Errorf("unexpected event: %+v", events[0])
	}
}

func TestTableGrants_BodyProjectIDIgnored(t *testing.T) {
	r, _, _ := setupGrantRouter(t)
	body := validGrant()
	body["projectId"] = "attacker-controlled"

	w := doGrantRequest(t, r, "POST", "/api/provision/proj-a/table-grants/", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	var got domain.TableGrant
	json.Unmarshal(w.Body.Bytes(), &got)
	if got.ProjectID != "proj-a" {
		t.Errorf("body projectId leaked through: %q", got.ProjectID)
	}
}

func TestTableGrants_Create_RejectsInvalidPayload(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(m map[string]any)
		wantSubstr string
	}{
		{"missing resource", func(m map[string]any) { m["resource"] = "" }, "resource"},
		{"resource with quote", func(m map[string]any) { m["resource"] = `orders"; DROP TABLE x --` }, "resource"},
		{"resource too many parts", func(m map[string]any) { m["resource"] = "a.b.c.d" }, "resource"},
		{"no operations", func(m map[string]any) { m["operations"] = []string{} }, "operation"},
		{"unknown operation", func(m map[string]any) { m["operations"] = []string{"TRUNCATE"} }, "operation"},
		{"missing role", func(m map[string]any) { m["role"] = "" }, "role"},
		{"role with space", func(m map[string]any) { m["role"] = "bad role" }, "role"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, bus := setupGrantRouter(t)
			body := validGrant()
			tc.mutate(body)
			w := doGrantRequest(t, r, "POST", "/api/provision/proj-a/table-grants/", body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d body=%s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantSubstr) {
				t.Errorf("error %q should mention %q", w.Body.String(), tc.wantSubstr)
			}
			if len(bus.snapshot()) != 0 {
				t.Error("rejected write must not publish a change event")
			}
		})
	}
}

func TestTableGrants_Create_RejectsInvalidProjectID(t *testing.T) {
	r, _, _ := setupGrantRouter(t)
	w := doGrantRequest(t, r, "GET", "/api/provision/..%2fetc/table-grants/", nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for a malformed projectId, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestTableGrants_ListReturnsStoredGrants(t *testing.T) {
	r, _, _ := setupGrantRouter(t)
	if w := doGrantRequest(t, r, "POST", "/api/provision/proj-a/table-grants/", validGrant()); w.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", w.Code, w.Body.String())
	}

	w := doGrantRequest(t, r, "GET", "/api/provision/proj-a/table-grants/", nil)
	set := decodeGrantSet(t, w)
	if len(set.Grants) != 1 || set.Grants[0].Resource != "public.orders" {
		t.Fatalf("unexpected list: %+v", set)
	}
	// Another project must not see it.
	other := decodeGrantSet(t, doGrantRequest(t, r, "GET", "/api/provision/proj-b/table-grants/", nil))
	if len(other.Grants) != 0 {
		t.Fatalf("grants leaked across projects: %+v", other.Grants)
	}
}

func TestTableGrants_UpdateAndDeleteEmitEvents(t *testing.T) {
	r, _, bus := setupGrantRouter(t)
	created := doGrantRequest(t, r, "POST", "/api/provision/proj-a/table-grants/", validGrant())
	var grant domain.TableGrant
	json.Unmarshal(created.Body.Bytes(), &grant)

	upd := doGrantRequest(t, r, "PATCH", "/api/provision/proj-a/table-grants/"+grant.ID,
		map[string]any{"operations": []string{"SELECT", "INSERT"}})
	if upd.Code != http.StatusOK {
		t.Fatalf("update: %d %s", upd.Code, upd.Body.String())
	}
	var updated domain.TableGrant
	json.Unmarshal(upd.Body.Bytes(), &updated)
	if len(updated.Operations) != 2 {
		t.Errorf("operations not updated: %+v", updated.Operations)
	}
	if updated.ID != grant.ID {
		t.Errorf("id changed on update: %q -> %q", grant.ID, updated.ID)
	}

	del := doGrantRequest(t, r, "DELETE", "/api/provision/proj-a/table-grants/"+grant.ID, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", del.Code, del.Body.String())
	}

	ops := []string{}
	for _, e := range bus.snapshot() {
		ops = append(ops, e.Op)
	}
	want := []string{"create", "update", "delete"}
	if len(ops) != len(want) {
		t.Fatalf("want events %v, got %v", want, ops)
	}
	for i := range want {
		if ops[i] != want[i] {
			t.Fatalf("want events %v, got %v", want, ops)
		}
	}
}

func TestTableGrants_UpdateAndDeleteMissingReturn404(t *testing.T) {
	r, _, _ := setupGrantRouter(t)
	if w := doGrantRequest(t, r, "PATCH", "/api/provision/proj-a/table-grants/nope",
		map[string]any{"enabled": false}); w.Code != http.StatusNotFound {
		t.Errorf("update missing: want 404, got %d", w.Code)
	}
	if w := doGrantRequest(t, r, "DELETE", "/api/provision/proj-a/table-grants/nope", nil); w.Code != http.StatusNotFound {
		t.Errorf("delete missing: want 404, got %d", w.Code)
	}
}

func TestTableGrants_CrossProjectGrantIsNotReachable(t *testing.T) {
	r, _, _ := setupGrantRouter(t)
	created := doGrantRequest(t, r, "POST", "/api/provision/proj-a/table-grants/", validGrant())
	var grant domain.TableGrant
	json.Unmarshal(created.Body.Bytes(), &grant)

	if w := doGrantRequest(t, r, "PATCH", "/api/provision/proj-evil/table-grants/"+grant.ID,
		map[string]any{"enabled": false}); w.Code != http.StatusNotFound {
		t.Errorf("another project must not update this grant: got %d", w.Code)
	}
	if w := doGrantRequest(t, r, "DELETE", "/api/provision/proj-evil/table-grants/"+grant.ID, nil); w.Code != http.StatusNotFound {
		t.Errorf("another project must not delete this grant: got %d", w.Code)
	}
}

func TestTableGrants_StoreFailureIsSanitized(t *testing.T) {
	r, store, _ := setupGrantRouter(t)
	store.failWith = errors.New("pq: relation \"table_grants\" does not exist\nDETAIL: secret internals")

	w := doGrantRequest(t, r, "GET", "/api/provision/proj-a/table-grants/", nil)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("want 500, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "DETAIL") || strings.Contains(w.Body.String(), "pq: ") {
		t.Errorf("driver internals leaked to the client: %s", w.Body.String())
	}
}

func TestTableGrants_PublisherIsOptional(t *testing.T) {
	// Handlers constructed without a publisher (unit tests, NATS-less dev)
	// must still serve writes rather than panicking.
	h := NewTableGrantHandler(newFakeGrantStore(), everyProject{}, true)
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}/table-grants", func(r chi.Router) { h.Routes(r) })

	if w := doGrantRequest(t, r, "POST", "/api/provision/proj-a/table-grants/", validGrant()); w.Code != http.StatusCreated {
		t.Fatalf("want 201 without a publisher, got %d body=%s", w.Code, w.Body.String())
	}
}
