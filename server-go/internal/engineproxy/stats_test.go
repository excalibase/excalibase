package engineproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// monitoredEngine answers inspects with a full container document, env
// included, so a test sees what the proxy lets through.
type monitoredEngine struct {
	mu      sync.Mutex
	reached []string
}

func (e *monitoredEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	e.reached = append(e.reached, r.Method+" "+r.URL.RequestURI())
	e.mu.Unlock()
	path := stripVersion(r.URL.Path)
	labels := map[string]map[string]string{
		"excalibase-p1-postgres": {"excalibase.managed": "true"},
		"excalibase-postgres":    {"com.docker.compose.service": "postgres"},
		"excalibase-other":       {},
	}
	switch {
	case strings.HasSuffix(path, "/json"):
		name := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
		own, ok := labels[name]
		if !ok {
			http.Error(w, `{"message":"No such container"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id": "id-" + name, "Name": "/" + name,
			"State":  map[string]any{"Status": "running", "Running": true, "Pid": 4242, "Health": map[string]any{"Status": "unhealthy", "Log": []any{map[string]any{"Output": "secret probe output"}}}},
			"Config": map[string]any{"Labels": own, "Env": []string{"POSTGRES_PASSWORD=hunter2"}, "Image": "postgres:17"},
			"Mounts": []any{map[string]any{"Source": "/var/lib/docker/volumes/x"}},
		})
	case strings.HasSuffix(path, "/stats"):
		_, _ = w.Write([]byte(`{"read":"2026-10-29T00:00:00Z","cpu_stats":{"cpu_usage":{"total_usage":1}}}`))
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (e *monitoredEngine) got(prefix string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, call := range e.reached {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func monitoredProxy(t *testing.T) (*monitoredEngine, string) {
	t.Helper()
	engine := &monitoredEngine{}
	upstream := httptest.NewServer(engine)
	t.Cleanup(upstream.Close)
	target, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	policy := testPolicy()
	policy.StatsContainers = []string{"excalibase-postgres"}
	proxy := httptest.NewServer(NewHandler(policy, target, http.DefaultTransport))
	t.Cleanup(proxy.Close)
	return engine, proxy.URL
}

func TestStatsAreForwardedOneShotForManagedAndListedPlatformContainers(t *testing.T) {
	engine, proxy := monitoredProxy(t)
	for _, path := range []string{
		"/v1.47/containers/excalibase-p1-postgres/stats?stream=false",
		"/v1.47/containers/excalibase-p1-postgres/stats?stream=0&one-shot=1",
		"/v1.41/containers/excalibase-postgres/stats?stream=false&one-shot=true",
	} {
		resp := call(t, proxy, http.MethodGet, path, "")
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "cpu_stats") {
			t.Errorf("GET %s = %d %s, want the engine's stats", path, resp.StatusCode, body)
		}
	}
	if !engine.got("GET /v1.41/containers/excalibase-postgres/stats?stream=false&one-shot=true") {
		t.Error("the platform container's stats never reached the engine")
	}
}

func TestStatsAreRefusedWhenStreamingOrForOtherContainers(t *testing.T) {
	_, proxy := monitoredProxy(t)
	for _, path := range []string{
		"/v1.47/containers/excalibase-p1-postgres/stats",
		"/v1.47/containers/excalibase-p1-postgres/stats?stream=true",
		"/v1.47/containers/excalibase-p1-postgres/stats?stream=false&stream=true",
		"/v1.47/containers/excalibase-p1-postgres/stats?stream=false&one-shot=maybe",
		"/v1.47/containers/excalibase-p1-postgres/stats?stream=false&extra=1",
		"/v1.47/containers/excalibase-other/stats?stream=false",
		"/v1.47/containers/id-excalibase-postgres/stats?stream=false",
	} {
		if resp := call(t, proxy, http.MethodGet, path, ""); resp.StatusCode < 400 {
			t.Errorf("GET %s = %d, want refused", path, resp.StatusCode)
		}
	}
}

func TestAListedPlatformContainerIsInspectedForItsStateAlone(t *testing.T) {
	_, proxy := monitoredProxy(t)
	resp := call(t, proxy, http.MethodGet, "/v1.47/containers/excalibase-postgres/json", "")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("inspect = %d %s", resp.StatusCode, body)
	}
	for _, leaked := range []string{"hunter2", "Env", "Mounts", "secret probe output", "Pid", "Labels"} {
		if strings.Contains(string(body), leaked) {
			t.Errorf("the platform inspect carries %q: %s", leaked, body)
		}
	}
	var state struct {
		ID    string `json:"Id"`
		Name  string
		State struct {
			Status  string
			Running bool
			Health  struct{ Status string }
		}
		Config struct{ Image string }
	}
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	if state.Name != "/excalibase-postgres" || !state.State.Running || state.State.Status != "running" ||
		state.State.Health.Status != "unhealthy" || state.Config.Image != "postgres:17" {
		t.Errorf("platform inspect = %+v", state)
	}
}

func TestAManagedContainerIsStillInspectedWhole(t *testing.T) {
	_, proxy := monitoredProxy(t)
	resp := call(t, proxy, http.MethodGet, "/v1.47/containers/excalibase-p1-postgres/json", "")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Env") {
		t.Errorf("managed inspect = %d %s, want the engine's answer", resp.StatusCode, body)
	}
}

func TestAListedPlatformContainerGetsNothingButStatsAndState(t *testing.T) {
	engine, proxy := monitoredProxy(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1.47/containers/excalibase-postgres/logs?stdout=1"},
		{http.MethodPost, "/v1.47/containers/excalibase-postgres/exec"},
		{http.MethodPost, "/v1.47/containers/excalibase-postgres/stop"},
		{http.MethodDelete, "/v1.47/containers/excalibase-postgres"},
		{http.MethodGet, "/v1.47/containers/excalibase-postgres/archive?path=/"},
		{http.MethodGet, "/v1.47/containers/excalibase-postgres/json?size=1"},
	} {
		if resp := call(t, proxy, tc.method, tc.path, ""); resp.StatusCode < 400 {
			t.Errorf("%s %s = %d, want refused", tc.method, tc.path, resp.StatusCode)
		}
	}
	if engine.got("GET /v1.47/containers/excalibase-postgres/logs") || engine.got("POST /v1.47/containers/excalibase-postgres") {
		t.Error("a refused platform call reached the engine")
	}
}

func TestACreateMayNotTakeAMonitoredPlatformName(t *testing.T) {
	policy := testPolicy()
	policy.StatsContainers = []string{"excalibase-postgres"}
	err := policy.CheckCreate("excalibase-postgres", []byte(`{"Labels":{"excalibase.managed":"true"},"HostConfig":{}}`))
	if err == nil {
		t.Fatal("a managed container took a platform container's name")
	}
}
