package engineproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testFirewall = "excalibase-host-firewall"

type restartContainer struct {
	labels map[string]string
	mode   string
	// networks are what the engine reports attached; empty is the mode alone.
	networks []string
	policy string
	status string
	health string
}

// restartEngine holds containers by id, with the engine's answers the supervisor reads.
type restartEngine struct {
	mu         sync.Mutex
	containers map[string]*restartContainer
	started    []string
	created    string
}

func (e *restartEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	path := stripVersion(r.URL.Path)
	switch {
	case r.Method == http.MethodGet && path == "/containers/json":
		e.list(w)
	case r.Method == http.MethodPost && path == "/containers/create":
		body, _ := io.ReadAll(r.Body)
		e.created = string(body)
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "new"})
	default:
		id, action, _ := strings.Cut(strings.TrimPrefix(path, "/containers/"), "/")
		found, ok := e.containers[id]
		if !ok {
			http.Error(w, `{"message":"No such container"}`, http.StatusNotFound)
			return
		}
		e.act(w, r, id, action, found)
	}
}

func (e *restartEngine) list(w http.ResponseWriter) {
	list := []map[string]any{}
	for id, found := range e.containers {
		if found.labels[ManagedLabel] != "true" {
			continue
		}
		list = append(list, map[string]any{"Id": id, "Labels": found.labels, "State": found.status,
			"HostConfig": map[string]any{"NetworkMode": found.mode}})
	}
	_ = json.NewEncoder(w).Encode(list)
}

func (e *restartEngine) act(w http.ResponseWriter, r *http.Request, id, action string, found *restartContainer) {
	switch action {
	case "json":
		state := map[string]any{"Status": found.status, "Running": found.status == "running"}
		if found.health != "" {
			state["Health"] = map[string]any{"Status": found.health}
		}
		attached := map[string]any{}
		for _, network := range found.networks {
			attached[network] = map[string]any{}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "State": state, "NetworkSettings": map[string]any{"Networks": attached},
			"Config":     map[string]any{"Labels": found.labels},
			"HostConfig": map[string]any{"NetworkMode": found.mode, "RestartPolicy": map[string]any{"Name": found.policy}}})
	case "start":
		found.status = "running"
		e.started = append(e.started, id)
		w.WriteHeader(http.StatusNoContent)
	case "stop":
		found.status = "exited"
		w.WriteHeader(http.StatusNoContent)
	case "update":
		var update struct{ RestartPolicy struct{ Name string } }
		_ = json.NewDecoder(r.Body).Decode(&update)
		found.policy = update.RestartPolicy.Name
		_, _ = w.Write([]byte(`{"Warnings":null}`))
	default:
		http.Error(w, "unexpected "+action, http.StatusTeapot)
	}
}

func (e *restartEngine) startedOnly() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string{}, e.started...)
}

func tenant(status string) *restartContainer {
	return &restartContainer{labels: map[string]string{ManagedLabel: "true"}, mode: "excalibase-tenants", policy: "no", status: status}
}

func firewall(status, health string) *restartContainer {
	return &restartContainer{labels: map[string]string{}, mode: "host", policy: "unless-stopped", status: status, health: health}
}

// restartProxy is a guarded handler on an engine holding containers, with its stop record in a temp file.
func restartProxy(t *testing.T, containers map[string]*restartContainer) (*restartEngine, *Handler, string) {
	t.Helper()
	engine := &restartEngine{containers: containers}
	upstream := httptest.NewServer(engine)
	t.Cleanup(upstream.Close)
	file := filepath.Join(t.TempDir(), "stopped")
	return engine, guarded(t, upstream.URL, file), file
}

func guarded(t *testing.T, engineURL, file string) *Handler {
	t.Helper()
	target, err := url.Parse(engineURL)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(testPolicy(), target, http.DefaultTransport).WithRestarts(Restarts{FirewallContainer: testFirewall, StoppedFile: file})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func serve(t *testing.T, handler *Handler, method, path, body string) *http.Response {
	t.Helper()
	proxy := httptest.NewServer(handler)
	t.Cleanup(proxy.Close)
	return call(t, proxy.URL, method, path, body)
}

func TestTenantsAreCreatedWithoutARestartPolicyTheEngineWouldActOn(t *testing.T) {
	for _, requested := range []string{"unless-stopped", "always", "on-failure"} {
		engine, handler, _ := restartProxy(t, map[string]*restartContainer{})
		body := createBody(t, func(b map[string]any) {
			hostConfig(b)["RestartPolicy"] = map[string]any{"Name": requested, "MaximumRetryCount": 0}
		})
		resp := serve(t, handler, http.MethodPost, "/containers/create?name=excalibase-p1-postgres", string(body))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: create answered %d", requested, resp.StatusCode)
		}
		var created struct {
			HostConfig struct {
				RestartPolicy struct{ Name string }
			}
		}
		if err := json.Unmarshal([]byte(engine.created), &created); err != nil {
			t.Fatalf("%s: %v in %s", requested, err, engine.created)
		}
		if created.HostConfig.RestartPolicy.Name != "no" {
			t.Errorf("%s reached the engine as %q", requested, created.HostConfig.RestartPolicy.Name)
		}
	}
}

// An app's network is internal and has no address on the host, firewall or not: the engine keeps restarting it.
func TestAppsOnAProjectNetworkKeepTheirRestartPolicy(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{})
	body := createBody(t, func(b map[string]any) {
		hostConfig(b)["NetworkMode"] = "excalibase-proj-p1"
		hostConfig(b)["PortBindings"] = nil
		b["NetworkingConfig"] = map[string]any{"EndpointsConfig": map[string]any{"excalibase-proj-p1": map[string]any{}}}
	})
	resp := serve(t, handler, http.MethodPost, "/containers/create?name=excalibase-p1-app", string(body))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create answered %d", resp.StatusCode)
	}
	if !strings.Contains(engine.created, `"unless-stopped"`) {
		t.Fatalf("an app lost its restart policy: %s", engine.created)
	}
}

// A project network named as the mode does not exempt a create that also joins a guarded one.
func TestACreateJoiningAGuardedNetworkIsHeldWhateverItsMode(t *testing.T) {
	if restartExempt(testPolicy(), "excalibase-proj-p1", []string{"excalibase-proj-p1", "excalibase-tenants"}) {
		t.Fatal("a create joining the tenants' network is exempt")
	}
	if !restartExempt(testPolicy(), "excalibase-proj-p1", []string{"excalibase-proj-p1"}) {
		t.Fatal("an app on its project network is held")
	}
}

func TestAStartIsRefusedUnlessTheFirewallIsHealthy(t *testing.T) {
	for name, wall := range map[string]*restartContainer{
		"unhealthy": firewall("running", "unhealthy"),
		"starting":  firewall("running", "starting"),
		"stopped":   firewall("exited", "unhealthy"),
		"no check":  firewall("running", ""),
		"missing":   nil,
	} {
		containers := map[string]*restartContainer{"excalibase-p1-postgres": tenant("exited")}
		if wall != nil {
			containers[testFirewall] = wall
		}
		engine, handler, _ := restartProxy(t, containers)
		resp := serve(t, handler, http.MethodPost, "/containers/excalibase-p1-postgres/start", "")
		if resp.StatusCode != http.StatusForbidden || len(engine.startedOnly()) != 0 {
			t.Errorf("%s: answered %d, started %v", name, resp.StatusCode, engine.startedOnly())
		}
	}
}

func TestAStartPassesBehindAHealthyFirewallAndAppsNeverWait(t *testing.T) {
	app := tenant("exited")
	app.mode = "excalibase-proj-p1"
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{
		"excalibase-p1-postgres": tenant("exited"), "excalibase-p1-app": app, testFirewall: firewall("running", "healthy"),
	})
	if resp := serve(t, handler, http.MethodPost, "/containers/excalibase-p1-postgres/start", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("tenant start answered %d", resp.StatusCode)
	}
	engine.containers[testFirewall].health = "unhealthy"
	if resp := serve(t, handler, http.MethodPost, "/containers/excalibase-p1-app/start", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("app start answered %d", resp.StatusCode)
	}
}

// A container on its project network that also joined a guarded one waits like any tenant.
func TestAStartOnAGuardedNetworkWaitsWhateverTheMode(t *testing.T) {
	both := tenant("exited")
	both.mode, both.networks = "excalibase-proj-p1", []string{"excalibase-proj-p1", "excalibase-tenants"}
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"excalibase-p1-app": both, testFirewall: firewall("running", "starting")})
	resp := serve(t, handler, http.MethodPost, "/containers/excalibase-p1-app/start", "")
	if resp.StatusCode != http.StatusForbidden || len(engine.startedOnly()) != 0 {
		t.Fatalf("answered %d, started %v", resp.StatusCode, engine.startedOnly())
	}
}

func TestSupervisorStartsWhatExitedOnItsOwn(t *testing.T) {
	buildStep := tenant("exited")
	buildStep.labels = map[string]string{ManagedLabel: "true", "excalibase.component": "build"}
	app := tenant("exited")
	app.mode = "excalibase-proj-p1"
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{
		"crashed": tenant("exited"), "running": tenant("running"), "never-started": tenant("created"),
		"build-step": buildStep, "app": app, testFirewall: firewall("running", "healthy"),
	})
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 1 || started[0] != "crashed" {
		t.Fatalf("started %v, want only the crashed tenant", started)
	}
}

func TestSupervisorStartsNothingUntilTheFirewallIsHealthy(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{
		"crashed": tenant("exited"), testFirewall: firewall("running", "starting"),
	})
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 0 {
		t.Fatalf("started %v behind a firewall still starting", started)
	}
	engine.containers[testFirewall].health = "healthy"
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 1 {
		t.Fatalf("started %v once the firewall is healthy", started)
	}
}

// A stop through the proxy is the one way a tenant stays down, and it is kept across a proxy restart (a reboot).
func TestAStoppedTenantStaysStoppedUntilStartedAgain(t *testing.T) {
	engine, handler, file := restartProxy(t, map[string]*restartContainer{
		"excalibase-p1-postgres": tenant("running"), testFirewall: firewall("running", "healthy"),
	})
	if resp := serve(t, handler, http.MethodPost, "/containers/excalibase-p1-postgres/stop", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop answered %d", resp.StatusCode)
	}
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 0 {
		t.Fatalf("started %v after a stop", started)
	}
	upstream := httptest.NewServer(engine)
	t.Cleanup(upstream.Close)
	rebooted := guarded(t, upstream.URL, file)
	rebooted.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 0 {
		t.Fatalf("started %v after a stop and a reboot", started)
	}
	if resp := serve(t, rebooted, http.MethodPost, "/containers/excalibase-p1-postgres/start", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start answered %d", resp.StatusCode)
	}
	engine.containers["excalibase-p1-postgres"].status = "exited" // a crash after the resume
	rebooted.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 2 {
		t.Fatalf("started %v: a resumed tenant that crashed stays down", started)
	}
}

// A start asked for while the firewall is down still ends the stop: the tenant follows once it is healthy.
func TestARefusedStartStillEndsAStop(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{
		"excalibase-p1-postgres": tenant("running"), testFirewall: firewall("running", "healthy"),
	})
	if resp := serve(t, handler, http.MethodPost, "/containers/excalibase-p1-postgres/stop", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop answered %d", resp.StatusCode)
	}
	engine.containers[testFirewall].health = "unhealthy"
	if resp := serve(t, handler, http.MethodPost, "/containers/excalibase-p1-postgres/start", ""); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("start answered %d", resp.StatusCode)
	}
	engine.containers[testFirewall].health = "healthy"
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 1 {
		t.Fatalf("started %v: the restart asked for was lost", started)
	}
}

// A stop that cannot be recorded would look like a crash and come back: it is refused.
func TestAStopThatCannotBeRecordedIsRefused(t *testing.T) {
	engine := &restartEngine{containers: map[string]*restartContainer{"excalibase-p1-postgres": tenant("running")}}
	upstream := httptest.NewServer(engine)
	t.Cleanup(upstream.Close)
	handler := guarded(t, upstream.URL, filepath.Join(t.TempDir(), "no-such-dir", "stopped"))
	resp := serve(t, handler, http.MethodPost, "/containers/excalibase-p1-postgres/stop", "")
	if resp.StatusCode == http.StatusNoContent || engine.containers["excalibase-p1-postgres"].status != "running" {
		t.Fatalf("answered %d, tenant %s", resp.StatusCode, engine.containers["excalibase-p1-postgres"].status)
	}
}

// Tenants an older release made still carry the engine's policy. Stopped under it, one was stopped on purpose.
func TestSupervisorTakesOverTenantsAnOlderReleaseMade(t *testing.T) {
	old := tenant("running")
	old.policy = "unless-stopped"
	paused := tenant("exited")
	paused.policy = "unless-stopped"
	app := tenant("running")
	app.mode, app.policy, app.labels = "excalibase-proj-p1", "unless-stopped", map[string]string{ManagedLabel: "true", "excalibase.component": "app"}
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{
		"old": old, "paused": paused, "app": app, testFirewall: firewall("running", "starting"),
	})
	handler.superviseOnce(context.Background())
	if old.policy != "no" || paused.policy != "no" {
		t.Fatalf("policies old=%s paused=%s, want them taken over even with the firewall starting", old.policy, paused.policy)
	}
	if app.policy != "unless-stopped" {
		t.Fatalf("an app lost its restart policy: %s", app.policy)
	}
	engine.containers[testFirewall].health = "healthy"
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 0 {
		t.Fatalf("started %v: a tenant stopped under the old policy stays stopped", started)
	}
}

// With no guard (rootless Podman) the proxy changes nothing.
func TestWithoutAGuardRestartPoliciesAndStartsPassThrough(t *testing.T) {
	engine := &restartEngine{containers: map[string]*restartContainer{"excalibase-p1-postgres": tenant("exited")}}
	upstream := httptest.NewServer(engine)
	t.Cleanup(upstream.Close)
	target, _ := url.Parse(upstream.URL)
	handler := NewHandler(testPolicy(), target, http.DefaultTransport)
	if resp := serve(t, handler, http.MethodPost, "/containers/create?name=excalibase-p2-postgres", string(createBody(t, nil))); resp.StatusCode != http.StatusOK {
		t.Fatalf("create answered %d", resp.StatusCode)
	}
	if !strings.Contains(engine.created, `"unless-stopped"`) {
		t.Fatalf("policy rewritten without a guard: %s", engine.created)
	}
	if resp := serve(t, handler, http.MethodPost, "/containers/excalibase-p1-postgres/start", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start answered %d", resp.StatusCode)
	}
}
