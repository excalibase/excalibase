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
	enforced map[string]bool
	failWith error
}

func newFakeGrantStore() *fakeGrantStore {
	return &fakeGrantStore{grants: map[string]domain.TableGrant{}, enforced: map[string]bool{}}
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

func (f *fakeGrantStore) IsExposureEnforced(_ context.Context, projectID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return false, f.failWith
	}
	return f.enforced[projectID], nil
}

func (f *fakeGrantStore) SetExposureEnforced(_ context.Context, projectID string, enforced bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return f.failWith
	}
	f.enforced[projectID] = enforced
	return nil
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
	store := newFakeGrantStore()
	bus := &grantBus{}
	h := NewTableGrantHandler(store)
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

// The reason this feature carries an explicit flag: a client MUST be able to
// tell an unconfigured project (do not enforce) from a configured project with
// nothing granted (deny everything). Both have zero grants on the wire.
func TestTableGrants_NotEnforcedIsDistinguishableFromEnforcedWithZeroGrants(t *testing.T) {
	r, store, _ := setupGrantRouter(t)

	// State 1 — exposure never configured for this project.
	w := doGrantRequest(t, r, "GET", "/api/provision/proj-untouched/table-grants/", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d body=%s", w.Code, w.Body.String())
	}
	unconfigured := decodeGrantSet(t, w)
	if unconfigured.Enforced {
		t.Error("project with no exposure setting must report enforced=false")
	}
	if len(unconfigured.Grants) != 0 {
		t.Errorf("expected zero grants, got %+v", unconfigured.Grants)
	}
	if !strings.Contains(w.Body.String(), `"enforced":false`) {
		t.Errorf("response must carry an explicit enforced flag: %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"grants":null`) {
		t.Errorf("grants must serialize as [], never null: %s", w.Body.String())
	}

	// State 2 — exposure enforced, nothing granted: deny everything.
	if err := store.SetExposureEnforced(context.Background(), "proj-locked", true); err != nil {
		t.Fatalf("seed enforcement: %v", err)
	}
	w2 := doGrantRequest(t, r, "GET", "/api/provision/proj-locked/table-grants/", nil)
	locked := decodeGrantSet(t, w2)
	if !locked.Enforced {
		t.Error("enforced project must report enforced=true")
	}
	if len(locked.Grants) != 0 {
		t.Errorf("expected zero grants, got %+v", locked.Grants)
	}

	// The two states must not be byte-identical — that ambiguity is the bug.
	if w.Body.String() == w2.Body.String() {
		t.Fatalf("not-enforced and enforced-with-zero-grants are indistinguishable: %s", w.Body.String())
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

func TestTableGrants_ToggleEnforcementPersistsAndEmitsEvent(t *testing.T) {
	r, _, bus := setupGrantRouter(t)

	w := doGrantRequest(t, r, "PUT", "/api/provision/proj-a/table-grants/enforcement",
		map[string]any{"enforced": true})
	if w.Code != http.StatusOK {
		t.Fatalf("toggle: %d %s", w.Code, w.Body.String())
	}
	set := decodeGrantSet(t, w)
	if !set.Enforced {
		t.Error("toggle response should report the new state")
	}

	after := decodeGrantSet(t, doGrantRequest(t, r, "GET", "/api/provision/proj-a/table-grants/", nil))
	if !after.Enforced {
		t.Error("enforcement did not persist")
	}

	events := bus.snapshot()
	if len(events) != 1 || events[0].Kind != domain.ExposureChangeKind || events[0].Op != "update" {
		t.Fatalf("expected one exposure event, got %+v", events)
	}

	off := doGrantRequest(t, r, "PUT", "/api/provision/proj-a/table-grants/enforcement",
		map[string]any{"enforced": false})
	if off.Code != http.StatusOK || decodeGrantSet(t, off).Enforced {
		t.Fatalf("disable: %d %s", off.Code, off.Body.String())
	}
}

func TestTableGrants_ToggleEnforcementRejectsMissingField(t *testing.T) {
	r, _, bus := setupGrantRouter(t)
	w := doGrantRequest(t, r, "PUT", "/api/provision/proj-a/table-grants/enforcement",
		map[string]any{})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("want 400 when enforced is omitted, got %d body=%s", w.Code, w.Body.String())
	}
	if len(bus.snapshot()) != 0 {
		t.Error("rejected toggle must not publish")
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
	h := NewTableGrantHandler(newFakeGrantStore())
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}/table-grants", func(r chi.Router) { h.Routes(r) })

	if w := doGrantRequest(t, r, "POST", "/api/provision/proj-a/table-grants/", validGrant()); w.Code != http.StatusCreated {
		t.Fatalf("want 201 without a publisher, got %d body=%s", w.Code, w.Body.String())
	}
}
