package handler

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/byoc"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/go-chi/chi/v5"
)

const (
	byocProject  = "proj_p1"
	byocHost     = "db.example.com"
	byocPublicIP = "203.0.113.5"
	byocFnPath   = testProj1FnPath
)

// scriptedResolver answers each lookup of a host from a queue; the last
// answer repeats. Mirrors the rebind fixture in internal/byoc/guard_test.go.
type scriptedResolver struct {
	mu      sync.Mutex
	answers map[string][]string
}

func (r *scriptedResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	queue := r.answers[host]
	if len(queue) == 0 {
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}
	answer := queue[0]
	if len(queue) > 1 {
		r.answers[host] = queue[1:]
	}
	return []netip.Addr{netip.MustParseAddr(answer)}, nil
}

func (r *scriptedResolver) set(host string, answers ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers[host] = answers
}

type byocFixture struct {
	handler  *FunctionHandler
	router   *chi.Mux
	store    *edgefn.FunctionStore
	scripts  *map[string]edgefn.DeployRequest
	resolver *scriptedResolver
	guard    *byoc.Guard
}

// newBYOCFixture registers a BYOC project whose host currently resolves to
// answers (in order) and wires a handler that deploys to a mock runtime.
func newBYOCFixture(t *testing.T, mode domain.DeploymentMode, answers ...string) *byocFixture {
	t.Helper()
	v := newFakeVault()
	v.data["projects/"+byocProject+"/credentials/excalibase_app"] = map[string]string{
		"host": byocHost, "port": "5432", "database": "app", "username": "u", "password": "p",
	}
	resolver := &scriptedResolver{answers: map[string][]string{}}
	resolver.set(byocHost, answers...)
	guard := byoc.NewGuard(byoc.Policy{}, resolver)

	runtime, scripts := mockFnRuntime(t)
	instStore := &inMemoryInstanceStore{insts: map[string]*domain.DatabaseInstance{
		byocProject: {ProjectID: byocProject, OrgID: "default", DeploymentMode: mode},
	}}
	store := edgefn.NewFunctionStore(t.TempDir())
	h := NewFunctionHandler(store, edgefn.NewSecretsStore(v), edgefn.NewRuntimeClient(runtime.URL, ""), instStore, nil, testAPIBase)
	h.SetVault(v)
	h.SetEgressGuard(guard)

	r := chi.NewRouter()
	r.Route(testFunctionsRoute, func(r chi.Router) { r.Post("/", h.Create) })
	return &byocFixture{handler: h, router: r, store: store, scripts: scripts, resolver: resolver, guard: guard}
}

func (f *byocFixture) create(t *testing.T, id string) int {
	t.Helper()
	body := map[string]any{
		"id": id, "name": id,
		"files": []map[string]string{{"path": testIndexTS, "content": testDefaultHandler}},
	}
	return doJSON(f.router, "POST", byocFnPath, body).Code
}

func TestFunctionHandler_Create_BYOC_SendsPinnedDSNToRuntime(t *testing.T) {
	f := newBYOCFixture(t, domain.ModeBYOC, byocPublicIP)

	if code := f.create(t, "pinned"); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	deploy, ok := (*f.scripts)[byocProject+"__pinned"]
	if !ok {
		t.Fatal("deploy not recorded")
	}
	want := "postgres://u:p@203.0.113.5:5432/app?sslmode=require"
	if got := deploy.Secrets["EXCALIBASE_DB_URL"]; got != want {
		t.Errorf("EXCALIBASE_DB_URL = %q, want %q", got, want)
	}
	if deploy.Secrets["EXCALIBASE_DB_HOST"] != byocHost {
		t.Errorf("EXCALIBASE_DB_HOST = %q, want the hostname for TLS", deploy.Secrets["EXCALIBASE_DB_HOST"])
	}
	if deploy.Secrets["BYOC_PINNED"] != "1" {
		t.Errorf("BYOC_PINNED = %q, want 1", deploy.Secrets["BYOC_PINNED"])
	}
}

func TestFunctionHandler_Create_BYOC_IPv6PinIsBracketed(t *testing.T) {
	f := newBYOCFixture(t, domain.ModeBYOC, "2001:db8::10")

	if code := f.create(t, "v6"); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	want := "postgres://u:p@[2001:db8::10]:5432/app?sslmode=require"
	if got := (*f.scripts)[byocProject+"__v6"].Secrets["EXCALIBASE_DB_URL"]; got != want {
		t.Errorf("EXCALIBASE_DB_URL = %q, want %q", got, want)
	}
}

func TestFunctionHandler_Create_BYOC_RefusesRebindToInternal(t *testing.T) {
	f := newBYOCFixture(t, domain.ModeBYOC, byocPublicIP, "10.0.0.5")
	if err := f.guard.ValidateHost(context.Background(), byocHost); err != nil {
		t.Fatalf("registration should pass on the public answer: %v", err)
	}

	code := f.create(t, "rebound")
	if code == http.StatusCreated {
		t.Fatal("deploy must be refused after the host rebinds to an internal address")
	}
	if len(*f.scripts) != 0 {
		t.Errorf("nothing must reach the runtime, got %v", *f.scripts)
	}
	if fn, _ := f.store.Get(byocProject, "rebound"); fn != nil {
		t.Error("refused deploy must not leave the function in the store")
	}
}

func TestFunctionHandler_Create_Managed_IsNotPinned(t *testing.T) {
	f := newBYOCFixture(t, domain.ModeK8s)

	if code := f.create(t, "managed"); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	secrets := (*f.scripts)[byocProject+"__managed"].Secrets
	if _, pinned := secrets["BYOC_PINNED"]; pinned {
		t.Error("managed projects must not be marked pinned")
	}
	want := "postgres://u:p@db.example.com:5432/app?sslmode=require"
	if secrets["EXCALIBASE_DB_URL"] != want {
		t.Errorf("managed DSN changed: %q", secrets["EXCALIBASE_DB_URL"])
	}
}

func TestFunctionHandler_ReplayDeploys_BYOC_PinsAndRefusesRebind(t *testing.T) {
	f := newBYOCFixture(t, domain.ModeBYOC, byocPublicIP, "169.254.169.254")
	if code := f.create(t, "fn"); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	f.resolver.set(byocHost, "203.0.113.6", "169.254.169.254")

	deploys, err := f.handler.ReplayDeploys(context.Background(), byocProject)
	if err != nil || len(deploys) != 1 {
		t.Fatalf("replay = %v, %v", deploys, err)
	}
	if got := deploys[0].Secrets["EXCALIBASE_DB_URL"]; got != "postgres://u:p@203.0.113.6:5432/app?sslmode=require" {
		t.Errorf("replay must re-pin to the current public answer, got %q", got)
	}

	deploys, err = f.handler.ReplayDeploys(context.Background(), byocProject)
	if err == nil || len(deploys) != 0 {
		t.Fatalf("replay after rebind must refuse and build nothing, got %v, %v", deploys, err)
	}
}

func TestFunctionHandler_RepinBYOC_RedeploysOnAddressChange(t *testing.T) {
	f := newBYOCFixture(t, domain.ModeBYOC, byocPublicIP)
	if code := f.create(t, "fn"); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}

	f.handler.RepinBYOC(context.Background())
	if got := (*f.scripts)[byocProject+"__fn"].Secrets["EXCALIBASE_DB_URL"]; got != "postgres://u:p@203.0.113.5:5432/app?sslmode=require" {
		t.Errorf("unchanged address must not change the deploy, got %q", got)
	}

	f.resolver.set(byocHost, "203.0.113.7")
	f.handler.RepinBYOC(context.Background())
	if got := (*f.scripts)[byocProject+"__fn"].Secrets["EXCALIBASE_DB_URL"]; got != "postgres://u:p@203.0.113.7:5432/app?sslmode=require" {
		t.Errorf("address change must be redeployed, got %q", got)
	}

	f.resolver.set(byocHost, "127.0.0.1")
	f.handler.RepinBYOC(context.Background())
	if got := (*f.scripts)[byocProject+"__fn"].Secrets["EXCALIBASE_DB_URL"]; got != "postgres://u:p@203.0.113.7:5432/app?sslmode=require" {
		t.Errorf("rebind to internal must keep the last good pin, got %q", got)
	}
}

func TestFunctionHandler_StartBYOCRepin_StopsCleanly(t *testing.T) {
	f := newBYOCFixture(t, domain.ModeBYOC, byocPublicIP)
	stop := f.handler.StartBYOCRepin(0)
	stop()
}
