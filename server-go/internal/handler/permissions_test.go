package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

const (
	permProjectID  = "proj-1"
	permBase       = "/api/provision/" + permProjectID
	permSelectPath = permBase + "/permissions/tables/public.orders/roles/user/select"
	trackedPath    = permBase + "/tracked-functions/"
)

// fakePermissionStore is an in-memory storage.PermissionStore.
type fakePermissionStore struct {
	mu          sync.Mutex
	perms       map[string]domain.TablePermission
	functions   map[string]domain.TrackedFunction
	fnPerms     map[string]domain.FunctionPermission
	version     int64
	failWith    error
	lastCreated bool
}

func newFakePermissionStore() *fakePermissionStore {
	return &fakePermissionStore{perms: map[string]domain.TablePermission{},
		functions: map[string]domain.TrackedFunction{}, fnPerms: map[string]domain.FunctionPermission{}}
}

func (f *fakePermissionStore) Document(_ context.Context, projectID string) (*domain.PermissionDocument, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return nil, f.failWith
	}
	doc := &domain.PermissionDocument{ProjectID: projectID, Version: f.version, Tables: []domain.TablePermissions{},
		Functions: []domain.TrackedFunction{}, FunctionPermissions: []domain.FunctionPermission{}}
	keys := make([]string, 0, len(f.perms))
	for key := range f.perms {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		p := f.perms[key]
		doc.Tables = append(doc.Tables, domain.TablePermissions{Table: p.Table, Role: p.Role, Select: p.Definition})
	}
	for _, fn := range f.functions {
		doc.Functions = append(doc.Functions, fn)
	}
	for _, fp := range f.fnPerms {
		doc.FunctionPermissions = append(doc.FunctionPermissions, fp)
	}
	return doc, nil
}

func (f *fakePermissionStore) PutPermission(_ context.Context, p domain.TablePermission) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != nil {
		return false, f.failWith
	}
	key := p.Table + "|" + p.Role + "|" + p.Operation
	_, existed := f.perms[key]
	f.perms[key] = p
	f.version++
	f.lastCreated = !existed
	return !existed, nil
}

func (f *fakePermissionStore) DeletePermission(_ context.Context, _, table, role, op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := table + "|" + role + "|" + op
	if _, ok := f.perms[key]; !ok {
		return storage.ErrPermissionNotFound
	}
	delete(f.perms, key)
	f.version++
	return nil
}

func (f *fakePermissionStore) TrackFunction(_ context.Context, fn domain.TrackedFunction) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.functions[fn.Function]; ok {
		return storage.ErrFunctionAlreadyTracked
	}
	f.functions[fn.Function] = fn
	return nil
}

func (f *fakePermissionStore) UntrackFunction(_ context.Context, _, function string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.functions[function]; !ok {
		return storage.ErrFunctionNotTracked
	}
	delete(f.functions, function)
	for key, fp := range f.fnPerms {
		if fp.Function == function {
			delete(f.fnPerms, key)
		}
	}
	return nil
}

func (f *fakePermissionStore) PutFunctionPermission(_ context.Context, _, function, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.functions[function]; !ok {
		return storage.ErrFunctionNotTracked
	}
	f.fnPerms[function+"|"+role] = domain.FunctionPermission{Function: function, Role: role}
	return nil
}

func (f *fakePermissionStore) DeleteFunctionPermission(_ context.Context, _, function, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.fnPerms[function+"|"+role]; !ok {
		return storage.ErrFunctionPermissionNotFound
	}
	delete(f.fnPerms, function+"|"+role)
	return nil
}

func (f *fakePermissionStore) LegacyPending(context.Context) ([]string, error) { return nil, nil }
func (f *fakePermissionStore) ImportLegacy(context.Context, string, domain.LegacyPermissionImport) error {
	return nil
}

type fakeInspector struct {
	details []schema.FunctionDetail
	err     error
	asked   string
}

func (f *fakeInspector) FunctionDetails(_ context.Context, projectID, schemaName, name string) ([]schema.FunctionDetail, error) {
	f.asked = projectID + ":" + schemaName + "." + name
	return f.details, f.err
}

type permissionProjects map[string]bool

func (k permissionProjects) FindByProjectID(id string) (*domain.DatabaseInstance, error) {
	if !k[id] {
		return nil, nil
	}
	return &domain.DatabaseInstance{ProjectID: id}, nil
}

type permissionFixture struct {
	router    http.Handler
	store     *fakePermissionStore
	inspector *fakeInspector
	published *recordingPublisher
}

func newPermissionFixture() *permissionFixture {
	store := newFakePermissionStore()
	inspector := &fakeInspector{details: []schema.FunctionDetail{{
		Schema: "public", Name: "search_orders", Kind: "f", Volatility: "STABLE", ReturnsSet: true,
		ReturnsTable: "public.orders", Args: []schema.FunctionArg{{Name: "session", Type: "jsonb"}},
	}}}
	published := &recordingPublisher{}
	h := NewPermissionHandler(store, permissionProjects{permProjectID: true}, inspector)
	h.SetPublisher(published)
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}", func(r chi.Router) {
		r.Route("/permissions", h.PermissionRoutes)
		r.Route("/tracked-functions", h.TrackedFunctionRoutes)
		r.Route("/function-permissions", h.FunctionPermissionRoutes)
	})
	return &permissionFixture{router: r, store: store, inspector: inspector, published: published}
}

func (f *permissionFixture) do(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func (f *permissionFixture) lastEvent(t *testing.T) domain.PolicyChangeEvent {
	t.Helper()
	if len(f.published.events) == 0 {
		t.Fatal("no change event published")
	}
	return f.published.events[len(f.published.events)-1]
}

func TestPermissionDocument_UnknownProjectIs404(t *testing.T) {
	f := newPermissionFixture()
	if w := f.do(http.MethodGet, "/api/provision/nobody/permissions/", ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown project = %d, want 404: %s", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodGet, "/api/provision/bad%20id/permissions/", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid project id = %d, want 400", w.Code)
	}
}

func TestPermissionDocument_ArraysAreNeverNull(t *testing.T) {
	f := newPermissionFixture()
	w := f.do(http.MethodGet, permBase+"/permissions/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	want := `{"projectId":"proj-1","version":0,"tables":[],"functions":[],"functionPermissions":[]}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if strings.Contains(w.Body.String(), "enforced") {
		t.Fatal("the document carries no enforced switch")
	}
}

func TestPermissionDocument_StoreFailureHidesDetails(t *testing.T) {
	f := newPermissionFixture()
	f.store.failWith = errors.New(`pq: relation "api_permissions" does not exist`)
	w := f.do(http.MethodGet, permBase+"/permissions/", "")
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "api_permissions") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}

func TestPutPermission_StoresTheNormalizedObjectAndPublishes(t *testing.T) {
	f := newPermissionFixture()
	w := f.do(http.MethodPut, permSelectPath, `{ "columns":"*", "filter": {"owner_id":{"_eq":"X-Excalibase-User-Id"}} }`)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	want := `{"columns":"*","filter":{"owner_id":{"_eq":"X-Excalibase-User-Id"}}}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Fatalf("answer %s, want %s", got, want)
	}
	stored := f.store.perms["public.orders|user|select"]
	if string(stored.Definition) != want || stored.ProjectID != permProjectID {
		t.Fatalf("stored %+v", stored)
	}
	evt := f.lastEvent(t)
	if evt.Kind != domain.PermissionChangeKind || evt.ProjectID != permProjectID || evt.Resource != "public.orders" ||
		evt.Op != domain.OpChangeCreate {
		t.Fatalf("event %+v", evt)
	}
	f.do(http.MethodPut, permSelectPath, `{"columns":"*","filter":{}}`)
	if evt := f.lastEvent(t); evt.Op != domain.OpChangeUpdate {
		t.Fatalf("replacing must publish an update, got %+v", evt)
	}
}

func TestPutPermission_Refusals(t *testing.T) {
	base := permBase + "/permissions/tables/"
	cases := []struct{ name, path, body string }{
		{"unqualified table", base + "orders/roles/user/select", `{"filter":{},"columns":"*"}`},
		{"mixed-case table", base + "public.Orders/roles/user/select", `{"filter":{},"columns":"*"}`},
		{"service role", base + "public.orders/roles/service/select", `{"filter":{},"columns":"*"}`},
		{"bad role", base + "public.orders/roles/Admin/select", `{"filter":{},"columns":"*"}`},
		{"bad operation", base + "public.orders/roles/user/upsert", `{"filter":{},"columns":"*"}`},
		{"wrong keys", permSelectPath, `{"check":{},"columns":"*"}`},
		{"bad expression", permSelectPath, `{"filter":{"a":{"_regex":"x"}},"columns":"*"}`},
		{"not json", permSelectPath, `{`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newPermissionFixture()
			if w := f.do(http.MethodPut, c.path, c.body); w.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", w.Code, w.Body.String())
			}
			if len(f.store.perms) != 0 || len(f.published.events) != 0 {
				t.Fatal("a refused write must store and publish nothing")
			}
		})
	}
}

func TestPutPermission_BodyTooLarge(t *testing.T) {
	f := newPermissionFixture()
	body := `{"filter":{},"columns":"*","x":"` + strings.Repeat("a", maxPermissionBody) + `"}`
	if w := f.do(http.MethodPut, permSelectPath, body); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", w.Code)
	}
}

func TestDeletePermission(t *testing.T) {
	f := newPermissionFixture()
	if w := f.do(http.MethodDelete, permSelectPath, ""); w.Code != http.StatusNotFound {
		t.Fatalf("absent delete = %d, want 404", w.Code)
	}
	f.do(http.MethodPut, permSelectPath, `{"filter":{},"columns":"*"}`)
	if w := f.do(http.MethodDelete, permSelectPath, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204: %s", w.Code, w.Body.String())
	}
	if evt := f.lastEvent(t); evt.Op != domain.OpChangeDelete || evt.Kind != domain.PermissionChangeKind {
		t.Fatalf("event %+v", evt)
	}
}

func TestTrackFunction_ExposureFollowsVolatility(t *testing.T) {
	for volatility, exposed := range map[string]string{"STABLE": "QUERY", "VOLATILE": "MUTATION"} {
		t.Run(volatility, func(t *testing.T) {
			f := newPermissionFixture()
			f.inspector.details[0].Volatility = volatility
			f.inspector.details[0].SecurityDefiner = true
			w := f.do(http.MethodPost, trackedPath, `{"function":"public.search_orders","sessionArgument":"session"}`)
			if w.Code != http.StatusCreated {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			want := fmt.Sprintf(`{"function":"public.search_orders","exposedAs":%q,"inferPermissions":true,`+
				`"sessionArgument":"session","securityDefiner":true}`, exposed)
			if got := strings.TrimSpace(w.Body.String()); got != want {
				t.Fatalf("answer %s, want %s", got, want)
			}
			if f.inspector.asked != permProjectID+":public.search_orders" {
				t.Fatalf("inspected %q", f.inspector.asked)
			}
			if evt := f.lastEvent(t); evt.Kind != domain.FunctionChangeKind || evt.Resource != "public.search_orders" {
				t.Fatalf("event %+v", evt)
			}
		})
	}
}

func TestTrackFunction_InferenceCanBeTurnedOff(t *testing.T) {
	f := newPermissionFixture()
	if w := f.do(http.MethodPost, trackedPath, `{"function":"public.search_orders","inferPermissions":false}`); w.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if f.store.functions["public.search_orders"].InferPermissions {
		t.Fatal("inferPermissions false was not stored")
	}
}

func TestTrackFunction_Refusals(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		mutate  func(*fakeInspector)
		status  int
		message string
	}{
		{"unqualified", `{"function":"search_orders"}`, nil, http.StatusBadRequest, "schema.name"},
		{"reserved schema", `{"function":"auth.users_fn"}`, nil, http.StatusBadRequest, "not served"},
		{"exposure is not chosen", `{"function":"public.search_orders","exposedAs":"QUERY"}`, nil, http.StatusBadRequest, "exposedAs"},
		{"not found", `{"function":"public.search_orders"}`, func(i *fakeInspector) { i.details = nil }, http.StatusBadRequest, "not found"},
		{"procedure", `{"function":"public.search_orders"}`, func(i *fakeInspector) { i.details[0].Kind = "p" }, http.StatusBadRequest, "procedure"},
		{"overloaded", `{"function":"public.search_orders"}`, func(i *fakeInspector) { i.details = append(i.details, i.details[0]) }, http.StatusBadRequest, "overloaded"},
		{"scalar result", `{"function":"public.search_orders"}`, func(i *fakeInspector) { i.details[0].ReturnsTable = "" }, http.StatusBadRequest, "table or view"},
		{"session argument not json", `{"function":"public.search_orders","sessionArgument":"term"}`, nil, http.StatusBadRequest, "session argument"},
		{"project not running", `{"function":"public.search_orders"}`, func(i *fakeInspector) { i.err = projectdb.ErrNotServable }, http.StatusConflict, "not running"},
		{"no database", `{"function":"public.search_orders"}`, func(i *fakeInspector) { i.err = domain.ErrNoDatabase }, http.StatusConflict, ""},
		{"database unreachable", `{"function":"public.search_orders"}`, func(i *fakeInspector) { i.err = errors.New("dial tcp 10.0.0.1: refused") }, http.StatusBadGateway, "could not inspect"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newPermissionFixture()
			if c.mutate != nil {
				c.mutate(f.inspector)
			}
			w := f.do(http.MethodPost, trackedPath, c.body)
			if w.Code != c.status || !strings.Contains(w.Body.String(), c.message) {
				t.Fatalf("got %d %s, want %d mentioning %q", w.Code, w.Body.String(), c.status, c.message)
			}
			if strings.Contains(w.Body.String(), "10.0.0.1") {
				t.Fatal("the answer leaks connection details")
			}
			if len(f.store.functions) != 0 || len(f.published.events) != 0 {
				t.Fatal("a refused track must store and publish nothing")
			}
		})
	}
}

func TestTrackFunction_TwiceIsAConflict(t *testing.T) {
	f := newPermissionFixture()
	f.do(http.MethodPost, trackedPath, `{"function":"public.search_orders"}`)
	if w := f.do(http.MethodPost, trackedPath, `{"function":"public.search_orders"}`); w.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409", w.Code)
	}
}

func TestUntrackFunction(t *testing.T) {
	f := newPermissionFixture()
	path := trackedPath + "public.search_orders"
	if w := f.do(http.MethodDelete, path, ""); w.Code != http.StatusNotFound {
		t.Fatalf("absent untrack = %d, want 404", w.Code)
	}
	f.do(http.MethodPost, trackedPath, `{"function":"public.search_orders"}`)
	f.do(http.MethodPut, permBase+"/function-permissions/public.search_orders/roles/editor", "")
	if w := f.do(http.MethodDelete, path, ""); w.Code != http.StatusNoContent {
		t.Fatalf("untrack = %d: %s", w.Code, w.Body.String())
	}
	if len(f.store.fnPerms) != 0 {
		t.Fatal("untracking must remove the function's permissions")
	}
	if evt := f.lastEvent(t); evt.Op != domain.OpChangeDelete || evt.Kind != domain.FunctionChangeKind {
		t.Fatalf("event %+v", evt)
	}
	if w := f.do(http.MethodDelete, trackedPath+"search_orders", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("unqualified untrack = %d, want 400", w.Code)
	}
}

func TestFunctionPermissions(t *testing.T) {
	f := newPermissionFixture()
	path := permBase + "/function-permissions/public.search_orders/roles/editor"
	if w := f.do(http.MethodPut, path, ""); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "not tracked") {
		t.Fatalf("untracked = %d %s, want 400", w.Code, w.Body.String())
	}
	f.do(http.MethodPost, trackedPath, `{"function":"public.search_orders"}`)
	w := f.do(http.MethodPut, path, "")
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != `{"function":"public.search_orders","role":"editor"}` {
		t.Fatalf("put = %d %s", w.Code, w.Body.String())
	}
	if evt := f.lastEvent(t); evt.Kind != domain.FunctionChangeKind || evt.PolicyID != "editor" {
		t.Fatalf("event %+v", evt)
	}
	if w := f.do(http.MethodPut, permBase+"/function-permissions/public.search_orders/roles/service", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("service role = %d, want 400", w.Code)
	}
	if w := f.do(http.MethodDelete, path, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d", w.Code)
	}
	if w := f.do(http.MethodDelete, path, ""); w.Code != http.StatusNotFound {
		t.Fatalf("second delete = %d, want 404", w.Code)
	}
}

func TestPermissionWrites_WithoutPublisherStillSucceed(t *testing.T) {
	store := newFakePermissionStore()
	h := NewPermissionHandler(store, permissionProjects{permProjectID: true}, &fakeInspector{})
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}/permissions", h.PermissionRoutes)
	req := httptest.NewRequest(http.MethodPut, permSelectPath, strings.NewReader(`{"filter":{},"columns":"*"}`))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var stored map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &stored); err != nil {
		t.Fatal(err)
	}
}
