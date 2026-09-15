package handler

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/go-chi/chi/v5"
)

const (
	testEgressProject = "proj_egress1"
	testEgressNS      = "default-proj_egress1"
	testEgressPath    = "/api/projects/" + testEgressProject + "/functions/egress"
)

// memEgressStore is the in-memory EgressStore for handler tests.
type memEgressStore struct {
	hosts map[string][]string
}

func (m *memEgressStore) GetEgressHosts(projectID string) ([]string, error) {
	if h, ok := m.hosts[projectID]; ok {
		return h, nil
	}
	return []string{}, nil
}

func (m *memEgressStore) SetEgressHosts(projectID string, hosts []string) error {
	m.hosts[projectID] = hosts
	return nil
}

type egressFixture struct {
	router  *chi.Mux
	handler *FunctionHandler
	k8s     *k8s.MockClient
	scripts *map[string]edgefn.DeployRequest
	egress  *memEgressStore
}

func setupEgressHandler(t *testing.T) egressFixture {
	t.Helper()
	store := edgefn.NewFunctionStore(t.TempDir())
	secrets := edgefn.NewSecretsStore(newFakeVault())
	runtime, scripts := mockFnRuntime(t)
	mockK8s := k8s.NewMockClient()
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		testEgressProject: {ProjectID: testEgressProject, OrgID: "default", Namespace: testEgressNS},
	}}
	h := NewFunctionHandler(store, secrets, nil, instStore, nil, testAPIBase)
	h.SetK8sClient(mockK8s, testDenoImage, "secret")
	h.SetRuntimeURLFn(func(_ string) string { return runtime.URL })
	egress := &memEgressStore{hosts: map[string][]string{}}
	h.SetEgressStore(egress)

	r := chi.NewRouter()
	r.Route(testFunctionsRoute, func(r chi.Router) {
		r.Post("/", h.Create)
		r.Get("/egress", h.GetEgress)
		r.Put("/egress", h.PutEgress)
	})
	return egressFixture{router: r, handler: h, k8s: mockK8s, scripts: scripts, egress: egress}
}

func decodeEgress(t *testing.T, body []byte) egressResponse {
	t.Helper()
	var out egressResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode egress response: %v (%s)", err, body)
	}
	return out
}

func deployEgressFn(t *testing.T, f egressFixture, id string) {
	t.Helper()
	body := map[string]interface{}{
		"id": id, "name": id,
		"files": []map[string]string{{"path": testIndexTS, "content": testDefaultHandler}},
	}
	if w := doJSON(f.router, "POST", "/api/projects/"+testEgressProject+"/functions/", body); w.Code != http.StatusCreated {
		t.Fatalf("create: %d body=%s", w.Code, w.Body.String())
	}
}

func TestEgress_DefaultIsNoEgress(t *testing.T) {
	f := setupEgressHandler(t)
	w := doJSON(f.router, "GET", testEgressPath, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	got := decodeEgress(t, w.Body.Bytes())
	if len(got.AllowedHosts) != 0 || len(got.DefaultHosts) != 0 || len(got.EffectiveHosts) != 0 {
		t.Fatalf("new project must have no egress, got %+v", got)
	}
	if got.AllowedHosts == nil || got.EffectiveHosts == nil {
		t.Fatalf("lists must serialise as [] not null: %s", w.Body.String())
	}
}

func TestEgress_PutValidatesEntries(t *testing.T) {
	f := setupEgressHandler(t)
	cases := []struct {
		name  string
		body  interface{}
		wantC int
	}{
		{"hostnames and ports", map[string][]string{"allowedHosts": {"api.stripe.com", "*.amazonaws.com:443"}}, http.StatusOK},
		{"empty list clears", map[string][]string{"allowedHosts": {}}, http.StatusOK},
		{"private ip", map[string][]string{"allowedHosts": {"10.0.0.5"}}, http.StatusBadRequest},
		{"metadata endpoint", map[string][]string{"allowedHosts": {"169.254.169.254"}}, http.StatusBadRequest},
		{"bare wildcard", map[string][]string{"allowedHosts": {"*"}}, http.StatusBadRequest},
		{"scheme", map[string][]string{"allowedHosts": {"https://api.stripe.com"}}, http.StatusBadRequest},
		{"cluster-internal name", map[string][]string{"allowedHosts": {"platform-db.excalibase-platform.svc.cluster.local"}}, http.StatusBadRequest},
		{"wrong shape", map[string]string{"allowedHosts": "api.stripe.com"}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doJSON(f.router, "PUT", testEgressPath, tc.body)
			if w.Code != tc.wantC {
				t.Fatalf("PUT %v: got %d want %d (%s)", tc.body, w.Code, tc.wantC, w.Body.String())
			}
		})
	}
}

func TestEgress_PutPersistsCanonicalListAndGetReadsItBack(t *testing.T) {
	f := setupEgressHandler(t)
	w := doJSON(f.router, "PUT", testEgressPath, map[string][]string{"allowedHosts": {" API.Stripe.com ", "*.amazonaws.com", "api.stripe.com"}})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	want := []string{"*.amazonaws.com", "api.stripe.com"}
	if got := decodeEgress(t, w.Body.Bytes()); !reflect.DeepEqual(got.AllowedHosts, want) {
		t.Fatalf("PUT response = %v want %v", got.AllowedHosts, want)
	}
	if !reflect.DeepEqual(f.egress.hosts[testEgressProject], want) {
		t.Fatalf("store = %v want %v", f.egress.hosts[testEgressProject], want)
	}
	w = doJSON(f.router, "GET", testEgressPath, nil)
	if got := decodeEgress(t, w.Body.Bytes()); !reflect.DeepEqual(got.EffectiveHosts, want) {
		t.Fatalf("GET effective = %v want %v", got.EffectiveHosts, want)
	}
}

func TestEgress_RejectedPutLeavesStoreUntouched(t *testing.T) {
	f := setupEgressHandler(t)
	f.egress.hosts[testEgressProject] = []string{"api.stripe.com"}
	if w := doJSON(f.router, "PUT", testEgressPath, map[string][]string{"allowedHosts": {"api.stripe.com", "127.0.0.1"}}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
	if got := f.egress.hosts[testEgressProject]; !reflect.DeepEqual(got, []string{"api.stripe.com"}) {
		t.Fatalf("store changed on a rejected PUT: %v", got)
	}
}

func TestEgress_OperatorDefaultsMergeIntoEffective(t *testing.T) {
	f := setupEgressHandler(t)
	f.handler.SetEgressDefaults([]string{"*.excalibase.io"})
	f.egress.hosts[testEgressProject] = []string{"api.stripe.com"}
	w := doJSON(f.router, "GET", testEgressPath, nil)
	got := decodeEgress(t, w.Body.Bytes())
	if !reflect.DeepEqual(got.DefaultHosts, []string{"*.excalibase.io"}) {
		t.Fatalf("defaults = %v", got.DefaultHosts)
	}
	if !reflect.DeepEqual(got.AllowedHosts, []string{"api.stripe.com"}) {
		t.Fatalf("project list must stay separate from defaults: %v", got.AllowedHosts)
	}
	if !reflect.DeepEqual(got.EffectiveHosts, []string{"*.excalibase.io", "api.stripe.com"}) {
		t.Fatalf("effective = %v", got.EffectiveHosts)
	}
}

func TestEgress_FirstDeployRendersEffectiveListIntoRuntimeSpecAndDeploy(t *testing.T) {
	f := setupEgressHandler(t)
	f.handler.SetEgressDefaults([]string{"*.excalibase.io"})
	f.egress.hosts[testEgressProject] = []string{"api.stripe.com"}
	deployEgressFn(t, f, "hello")

	want := []string{"*.excalibase.io", "api.stripe.com"}
	if spec := f.k8s.DenoSpecs[testEgressNS]; !reflect.DeepEqual(spec.AllowedHosts, want) {
		t.Fatalf("runtime spec AllowedHosts = %v want %v", spec.AllowedHosts, want)
	}
	if req := (*f.scripts)[testEgressProject+"__hello"]; !reflect.DeepEqual(req.AllowedHosts, want) {
		t.Fatalf("deploy request AllowedHosts = %v want %v", req.AllowedHosts, want)
	}
}

func TestEgress_PutReRendersLiveRuntimeAndRedeploysFunctions(t *testing.T) {
	f := setupEgressHandler(t)
	deployEgressFn(t, f, "hello")
	if got := (*f.scripts)[testEgressProject+"__hello"].AllowedHosts; len(got) != 0 {
		t.Fatalf("precondition: no egress yet, got %v", got)
	}
	ensureCalls := len(f.k8s.Calls)

	w := doJSON(f.router, "PUT", testEgressPath, map[string][]string{"allowedHosts": {"api.stripe.com:443"}})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	want := []string{"api.stripe.com:443"}
	if spec := f.k8s.DenoSpecs[testEgressNS]; !reflect.DeepEqual(spec.AllowedHosts, want) {
		t.Fatalf("live runtime not re-rendered: spec=%v calls=%v", spec.AllowedHosts, f.k8s.Calls[ensureCalls:])
	}
	if req := (*f.scripts)[testEgressProject+"__hello"]; !reflect.DeepEqual(req.AllowedHosts, want) {
		t.Fatalf("function not redeployed with the new allowlist: %v", req.AllowedHosts)
	}
}

func TestEgress_PutWithoutRuntimeDoesNotProvisionOne(t *testing.T) {
	f := setupEgressHandler(t)
	w := doJSON(f.router, "PUT", testEgressPath, map[string][]string{"allowedHosts": {"api.stripe.com"}})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", w.Code, w.Body.String())
	}
	if f.k8s.DenoRuntimes[testEgressNS] {
		t.Fatalf("a project with no functions must not get a runtime pod on PUT; calls=%v", f.k8s.Calls)
	}
}

func TestEgress_WithoutStoreIsUnavailable(t *testing.T) {
	f := setupEgressHandler(t)
	f.handler.SetEgressStore(nil)
	if w := doJSON(f.router, "GET", testEgressPath, nil); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET without store: %d", w.Code)
	}
	if w := doJSON(f.router, "PUT", testEgressPath, map[string][]string{"allowedHosts": {"api.stripe.com"}}); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT without store: %d", w.Code)
	}
}
