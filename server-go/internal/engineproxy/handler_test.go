package engineproxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeEngine answers inspects from a fixed table and records every request
// that reached it, so a test can tell a refused call from a forwarded one.
type fakeEngine struct {
	mu         sync.Mutex
	forwarded  []string
	containers map[string]map[string]string // name or id -> labels
	execs      map[string]string            // exec id -> container
}

func (f *fakeEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.forwarded = append(f.forwarded, r.Method+" "+r.URL.Path)
	f.mu.Unlock()
	path := stripVersion(r.URL.Path)
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json"):
		ref := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
		labels, ok := f.containers[ref]
		if !ok {
			http.Error(w, `{"message":"No such container"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": ref, "Config": map[string]any{"Labels": labels}})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/exec/") && strings.HasSuffix(path, "/json"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/exec/"), "/json")
		owner, ok := f.execs[id]
		if !ok {
			http.Error(w, `{"message":"No such exec instance"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ID": id, "ContainerID": owner})
	default:
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Echo-Body", string(body))
		w.WriteHeader(http.StatusOK)
	}
}

func (f *fakeEngine) reached(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.forwarded {
		if c == call {
			return true
		}
	}
	return false
}

func newProxy(t *testing.T) (*fakeEngine, *httptest.Server) {
	t.Helper()
	engine := &fakeEngine{
		containers: map[string]map[string]string{
			"excalibase-p1-postgres":  {"excalibase.managed": "true"},
			"excalibase-provisioning": {"com.docker.compose.service": "provisioning"},
		},
		execs: map[string]string{"exec-managed": "excalibase-p1-postgres", "exec-platform": "excalibase-provisioning"},
	}
	upstream := httptest.NewServer(engine)
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(NewHandler(testPolicy(), target, http.DefaultTransport))
	t.Cleanup(proxy.Close)
	return engine, proxy
}

func call(t *testing.T, base, method, path, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, base+path, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestHandlerForwardsTheCallsTheProvisionerMakes(t *testing.T) {
	engine, proxy := newProxy(t)
	allowed := []struct{ method, path string }{
		{http.MethodGet, "/_ping"},
		{http.MethodHead, "/_ping"},
		{http.MethodGet, "/v1.47/version"},
		{http.MethodGet, "/v1.47/images/json"},
		{http.MethodPost, "/v1.47/images/create?fromImage=postgres&tag=17"},
		{http.MethodGet, "/v1.47/containers/excalibase-p1-postgres/json"},
		{http.MethodPost, "/v1.47/containers/excalibase-p1-postgres/start"},
		{http.MethodPost, "/v1.47/containers/excalibase-p1-postgres/stop?t=30"},
		{http.MethodDelete, "/v1.47/containers/excalibase-p1-postgres?force=1&v=1"},
		{http.MethodPut, "/v1.47/containers/excalibase-p1-postgres/archive?path=/var/lib/postgresql/data"},
		{http.MethodGet, "/v1.47/containers/excalibase-p1-postgres/archive?path=/walarchive"},
		{http.MethodPost, "/v1.47/exec/exec-managed/start"},
		{http.MethodGet, "/v1.47/exec/exec-managed/json"},
	}
	for _, tc := range allowed {
		resp := call(t, proxy.URL, tc.method, tc.path, "{}")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s %s = %d, want forwarded", tc.method, tc.path, resp.StatusCode)
		}
	}
	if !engine.reached("POST /v1.47/containers/excalibase-p1-postgres/start") {
		t.Fatal("start never reached the engine")
	}
}

func TestHandlerRefusesEverythingElse(t *testing.T) {
	engine, proxy := newProxy(t)
	refused := []struct{ method, path string }{
		{http.MethodGet, "/v1.47/containers/json"},
		{http.MethodPost, "/v1.47/containers/excalibase-provisioning/stop"},
		{http.MethodDelete, "/v1.47/containers/excalibase-provisioning"},
		{http.MethodGet, "/v1.47/containers/excalibase-provisioning/archive?path=/var/lib/excalibase"},
		{http.MethodPost, "/v1.47/containers/excalibase-provisioning/exec"},
		{http.MethodPost, "/v1.47/exec/exec-platform/start"},
		{http.MethodGet, "/v1.47/containers/unknown/json"},
		{http.MethodPost, "/v1.47/containers/excalibase-p1-postgres/update"},
		{http.MethodGet, "/v1.47/info"},
		{http.MethodPost, "/v1.47/volumes/create"},
		{http.MethodPost, "/v1.47/networks/create"},
		{http.MethodPost, "/v1.47/build"},
		{http.MethodDelete, "/v1.47/images/postgres:17"},
		{http.MethodPost, "/v1.47/swarm/init"},
		{http.MethodPost, "/v1.47/containers/../../swarm/init"},
		{http.MethodPost, "/v1.47/containers/excalibase-p1-postgres%2Fstart"},
	}
	for _, tc := range refused {
		resp := call(t, proxy.URL, tc.method, tc.path, "{}")
		if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s %s = %d, want refused", tc.method, tc.path, resp.StatusCode)
		}
	}
	for _, mutating := range []string{"POST /v1.47/containers/excalibase-provisioning/stop", "DELETE /v1.47/containers/excalibase-provisioning", "POST /v1.47/exec/exec-platform/start"} {
		if engine.reached(mutating) {
			t.Errorf("%s reached the engine", mutating)
		}
	}
}

func TestHandlerChecksTheCreateBodyAndForwardsItUnchanged(t *testing.T) {
	engine, proxy := newProxy(t)
	body := string(createBody(t, nil))
	resp := call(t, proxy.URL, http.MethodPost, "/v1.47/containers/create?name=excalibase-p1-postgres", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Echo-Body"); got != body {
		t.Fatalf("engine got a different body:\n%s", got)
	}

	bad := string(createBody(t, func(b map[string]any) { hostConfig(b)["Privileged"] = true }))
	resp = call(t, proxy.URL, http.MethodPost, "/v1.47/containers/create?name=excalibase-p2-postgres", bad)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("privileged create = %d, want 403", resp.StatusCode)
	}
	var refusal struct{ Message string }
	if err := json.NewDecoder(resp.Body).Decode(&refusal); err != nil || !strings.Contains(refusal.Message, "Privileged") {
		t.Fatalf("refusal %q does not say why (%v)", refusal.Message, err)
	}
	if creates := strings.Count(strings.Join(engine.forwarded, ","), "containers/create"); creates != 1 {
		t.Fatalf("engine saw %d creates, want only the allowed one", creates)
	}
}

func TestHandlerChecksExecCreateOnManagedContainersOnly(t *testing.T) {
	_, proxy := newProxy(t)
	if resp := call(t, proxy.URL, http.MethodPost, "/v1.47/containers/excalibase-p1-postgres/exec", `{"Cmd":["pg_isready"]}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("exec create on a tenant = %d", resp.StatusCode)
	}
	if resp := call(t, proxy.URL, http.MethodPost, "/v1.47/containers/excalibase-p1-postgres/exec", `{"Cmd":["sh"],"Privileged":true}`); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("privileged exec = %d, want 403", resp.StatusCode)
	}
}

// A second name could be the one another engine reads.
func TestHandlerRefusesACreateNamedTwice(t *testing.T) {
	_, proxy := newProxy(t)
	resp := call(t, proxy.URL, http.MethodPost, "/v1.47/containers/create?name=excalibase-p1-x&name=provisioning", string(createBody(t, nil)))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("create named twice = %d, want 403", resp.StatusCode)
	}
}
