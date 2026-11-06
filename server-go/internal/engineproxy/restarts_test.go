package engineproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const testFirewall = "excalibase-host-firewall"

type restartContainer struct {
	labels map[string]string
	mode   string
	// networks are what the engine reports attached; empty is the mode alone.
	networks []string
	policy   string
	status   string
	health   string
}

// restartEngine holds containers by id, with the engine's answers the supervisor reads.
type restartEngine struct {
	mu         sync.Mutex
	containers map[string]*restartContainer
	started    []string
	created    string
	// stopAnswer answers a stop, for an engine that fails one.
	stopAnswer int
	// onStart runs on each start, under the lock; hang blocks those ids until their caller gives up.
	onStart func(id string)
	hang    map[string]bool
	// onList runs once a list is answered, as if the engine moved on right after it.
	onList func()
}

func (e *restartEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	path := stripVersion(r.URL.Path)
	switch {
	case r.Method == http.MethodGet && path == "/containers/json":
		e.list(w)
		if e.onList != nil {
			e.onList()
		}
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
		if e.hang[id] {
			e.mu.Unlock()
			<-r.Context().Done()
			e.mu.Lock()
			return
		}
		if e.onStart != nil {
			e.onStart(id)
		}
		found.status = "running"
		e.started = append(e.started, id)
		w.WriteHeader(http.StatusNoContent)
	case "stop":
		if e.stopAnswer != 0 {
			http.Error(w, `{"message":"cannot stop"}`, e.stopAnswer)
			return
		}
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
		"stopped":   firewall("exited", "healthy"), // an engine keeps the last health of what exited
		"no check":  firewall("running", ""),
		"missing":   nil,
	} {
		containers := map[string]*restartContainer{"excalibase-p1-postgres": tenant("exited")}
		if wall != nil {
			containers[testFirewall] = wall
		}
		engine, handler, _ := restartProxy(t, containers)
		resp := serve(t, handler, http.MethodPost, "/containers/excalibase-p1-postgres/start", "")
		if resp.StatusCode != http.StatusServiceUnavailable || len(engine.startedOnly()) != 0 {
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
	if resp.StatusCode != http.StatusServiceUnavailable || len(engine.startedOnly()) != 0 {
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
	if resp := serve(t, handler, http.MethodPost, "/containers/excalibase-p1-postgres/start", ""); resp.StatusCode != http.StatusServiceUnavailable {
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

// Finding: a stop the engine failed leaves a running tenant that nothing would restart.
func TestAStopTheEngineFailedIsNotRecorded(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{
		"excalibase-p1-postgres": tenant("running"), testFirewall: firewall("running", "healthy"),
	})
	engine.stopAnswer = http.StatusInternalServerError
	if resp := serve(t, handler, http.MethodPost, "/containers/excalibase-p1-postgres/stop", ""); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("stop answered %d", resp.StatusCode)
	}
	if handler.restarts.isStopped("excalibase-p1-postgres") {
		t.Fatal("a failed stop stays on the record")
	}
	engine.containers["excalibase-p1-postgres"].status = "exited"
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 1 {
		t.Fatalf("started %v: the crash after a failed stop was lost", started)
	}
}

// A failed stop of what was already stopped on purpose keeps that record.
func TestAFailedStopKeepsAnEarlierRecord(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"excalibase-p1-postgres": tenant("exited")})
	if err := handler.restarts.setStopped("excalibase-p1-postgres", true); err != nil {
		t.Fatal(err)
	}
	engine.stopAnswer = http.StatusInternalServerError
	serve(t, handler, http.MethodPost, "/containers/excalibase-p1-postgres/stop", "")
	if !handler.restarts.isStopped("excalibase-p1-postgres") {
		t.Fatal("a failed stop forgot an earlier stop")
	}
}

func TestStopRecordSurvivesARestartExactly(t *testing.T) {
	engine, handler, file := restartProxy(t, map[string]*restartContainer{"a": tenant("exited"), "b": tenant("exited")})
	for _, id := range []string{"a", "b"} {
		if err := handler.restarts.setStopped(id, true); err != nil {
			t.Fatal(err)
		}
	}
	upstream := httptest.NewServer(engine)
	t.Cleanup(upstream.Close)
	again := guarded(t, upstream.URL, file)
	if !again.restarts.isStopped("a") || !again.restarts.isStopped("b") || again.restarts.corrupt {
		t.Fatalf("record after a restart: %+v", again.restarts.stopped)
	}
}

// Whatever is left of the file, a reboot starts nothing it may have been told to keep down.
func TestAnUnreadableStopRecordHoldsTheSupervisorBack(t *testing.T) {
	for name, content := range map[string]string{
		"empty":     "",
		"truncated": stoppedHeader + "\nabc\n",
		"garbage":   "\x00\x00\x00\x00",
		"no header": "abc\n" + stoppedEnd + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			old, oldStopped := tenant("running"), tenant("exited")
			old.policy, oldStopped.policy = "unless-stopped", "unless-stopped"
			engine := &restartEngine{containers: map[string]*restartContainer{"crashed": tenant("exited"), "old": old, "old-stopped": oldStopped, testFirewall: firewall("running", "healthy")}}
			upstream := httptest.NewServer(engine)
			t.Cleanup(upstream.Close)
			file := filepath.Join(t.TempDir(), "stopped")
			if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			handler := guarded(t, upstream.URL, file)
			handler.superviseOnce(context.Background())
			if started := engine.startedOnly(); len(started) != 0 {
				t.Fatalf("started %v from a record it cannot read", started)
			}
			if old.policy != "no" || oldStopped.policy != "no" {
				t.Fatalf("older tenants keep %s, %s: a reboot would start them unguarded", old.policy, oldStopped.policy)
			}
			// Nothing starts on its own in this mode, so a stop needs no record: pause, delete and a watcher's replacement go on working.
			if resp := serve(t, handler, http.MethodPost, "/containers/crashed/stop", ""); resp.StatusCode != http.StatusNoContent {
				t.Fatalf("stop answered %d", resp.StatusCode)
			}
			if got, _ := os.ReadFile(file); string(got) != content {
				t.Fatalf("the unreadable record was overwritten: %q", got)
			}
		})
	}
}

// A rename is only durable once the data and the directory are on disk.
func TestStopRecordIsSyncedBeforeAndAfterTheRename(t *testing.T) {
	_, handler, file := restartProxy(t, map[string]*restartContainer{})
	var synced []string
	before := syncFile
	syncFile = func(f *os.File) error {
		_, statErr := os.Stat(file)
		synced = append(synced, f.Name()+":"+strconv.FormatBool(statErr == nil))
		return f.Sync()
	}
	t.Cleanup(func() { syncFile = before })
	if err := handler.restarts.setStopped("a", true); err != nil {
		t.Fatal(err)
	}
	if len(synced) != 2 || !strings.HasSuffix(synced[0], ":false") || synced[1] != filepath.Dir(file)+":true" {
		t.Fatalf("synced %v", synced)
	}
}

// A firewall that goes down during a pass stops the rest of the pass.
func TestEachStartChecksTheFirewallAgain(t *testing.T) {
	wall := firewall("running", "healthy")
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"a": tenant("exited"), "b": tenant("exited"), testFirewall: wall})
	handler.restarts.workers = 1
	engine.onStart = func(string) { wall.health = "unhealthy" }
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 1 {
		t.Fatalf("started %v after the firewall went down", started)
	}
}

func TestAHungStartHoldsNoOtherTenantBack(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{
		"hung": tenant("exited"), "fine": tenant("exited"), testFirewall: firewall("running", "healthy"),
	})
	engine.hang = map[string]bool{"hung": true}
	handler.restarts.startTimeout = 2 * time.Second
	var startedAfter time.Duration
	began := time.Now()
	engine.onStart = func(string) { startedAfter = time.Since(began) }
	handler.startAll(context.Background(), []string{"hung", "fine"})
	if started := engine.startedOnly(); len(started) != 1 || started[0] != "fine" {
		t.Fatalf("started %v", started)
	}
	if startedAfter > time.Second {
		t.Fatalf("the other tenant waited %s behind a hung start", startedAfter)
	}
	if elapsed := time.Since(began); elapsed > 4*time.Second {
		t.Fatalf("a hung start held the pass for %s", elapsed)
	}
}

// Disk tools and build steps on no network have nothing a firewall guards.
func TestAContainerOnNoNetworkNeverWaits(t *testing.T) {
	step := tenant("exited")
	step.mode, step.networks = "none", []string{"none"}
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"excalibase-build-1-prepare": step, testFirewall: firewall("running", "starting")})
	if resp := serve(t, handler, http.MethodPost, "/containers/excalibase-build-1-prepare/start", ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("start answered %d", resp.StatusCode)
	}
	if !restartExempt(testPolicy(), "none", nil) || restartExempt(testPolicy(), "none", []string{"excalibase-tenants"}) {
		t.Fatal("network none is exempt alone")
	}
	if supervised(testPolicy(), map[string]string{ManagedLabel: "true"}, "none") {
		t.Fatal("a container on no network is supervised")
	}
	if len(engine.startedOnly()) != 1 {
		t.Fatalf("started %v", engine.startedOnly())
	}
}

// The refusal promises the start follows: that holds for one never started too.
func TestARefusedStartOfANewTenantFollowsTheFirewall(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{
		"excalibase-p1-postgres": tenant("created"), "never-asked": tenant("created"), testFirewall: firewall("running", "starting"),
	})
	if resp := serve(t, handler, http.MethodPost, "/containers/excalibase-p1-postgres/start", ""); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("start answered %d", resp.StatusCode)
	}
	engine.containers[testFirewall].health = "healthy"
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 1 || started[0] != "excalibase-p1-postgres" {
		t.Fatalf("started %v", started)
	}
}

// Started by hand after a stop through the proxy, it is looked after again.
func TestARunningTenantWithAFinishedStopLeavesTheRecord(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{
		"excalibase-p1-postgres": tenant("running"), testFirewall: firewall("running", "healthy"),
	})
	serve(t, handler, http.MethodPost, "/containers/excalibase-p1-postgres/stop", "")
	engine.containers["excalibase-p1-postgres"].status = "running" // docker start, not through the proxy
	handler.superviseOnce(context.Background())
	engine.containers["excalibase-p1-postgres"].status = "exited"
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 1 {
		t.Fatalf("started %v: a tenant started by hand stays down after a crash", started)
	}
}

// Still running because its stop has not answered yet: the record must stand.
func TestAStopInFlightKeepsTheRecord(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{
		"excalibase-p1-postgres": tenant("running"), testFirewall: firewall("running", "healthy"),
	})
	req := httptest.NewRequest(http.MethodPost, "/containers/excalibase-p1-postgres/stop", nil)
	if err := handler.beforeStop(req, "excalibase-p1-postgres"); err != nil {
		t.Fatal(err)
	}
	handler.superviseOnce(context.Background())
	engine.containers["excalibase-p1-postgres"].status = "exited"
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 0 {
		t.Fatalf("started %v during its own stop", started)
	}
}

func TestSupervisedIsAManagedContainerOfItsOwn(t *testing.T) {
	p := testPolicy()
	if supervised(p, map[string]string{}, "excalibase-tenants") {
		t.Error("an unmanaged container is supervised")
	}
	if !supervised(p, map[string]string{ManagedLabel: "true"}, "excalibase-tenants") {
		t.Error("a tenant is not supervised")
	}
}

// Removed containers leave both records.
func TestForgetDropsWhatTheEngineNoLongerHolds(t *testing.T) {
	engine, handler, file := restartProxy(t, map[string]*restartContainer{"gone": tenant("exited"), "kept": tenant("exited"), testFirewall: firewall("running", "healthy")})
	for _, id := range []string{"gone", "kept"} {
		if err := handler.restarts.setStopped(id, true); err != nil {
			t.Fatal(err)
		}
	}
	handler.superviseOnce(context.Background())
	if !handler.restarts.adopted["gone"] {
		t.Fatal("not checked for an older policy")
	}
	delete(engine.containers, "gone")
	handler.superviseOnce(context.Background())
	if handler.restarts.isStopped("gone") || handler.restarts.adopted["gone"] || !handler.restarts.isStopped("kept") {
		t.Fatalf("stopped %v adopted %v", handler.restarts.stopped, handler.restarts.adopted)
	}
	if raw, _ := os.ReadFile(file); strings.Contains(string(raw), "gone") || !strings.Contains(string(raw), "kept") {
		t.Fatalf("file %q", raw)
	}
}

func TestEngineRootlessReadsTheEngine(t *testing.T) {
	for options, want := range map[string]bool{`["name=seccomp","name=rootless"]`: true, `["name=seccomp"]`: false} {
		engine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"SecurityOptions":` + options + `}`))
		}))
		t.Cleanup(engine.Close)
		target, _ := url.Parse(engine.URL)
		got, err := NewHandler(testPolicy(), target, http.DefaultTransport).EngineRootless(context.Background())
		if err != nil || got != want {
			t.Errorf("%s: got %v, %v", options, got, err)
		}
	}
}

// A client that hangs up does not stop the engine, which finishes the stop anyway.
func TestAStopWhoseClientHungUpStaysRecorded(t *testing.T) {
	engine := &slowStopEngine{restartEngine: &restartEngine{containers: map[string]*restartContainer{
		"paused": tenant("running"), testFirewall: firewall("running", "healthy")}}, stopTakes: 300 * time.Millisecond}
	upstream := httptest.NewServer(engine)
	t.Cleanup(upstream.Close)
	handler := guarded(t, upstream.URL, filepath.Join(t.TempDir(), "stopped"))
	front := httptest.NewServer(handler)
	t.Cleanup(front.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, front.URL+"/containers/paused/stop", nil)
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("the client was meant to give up")
	}
	time.Sleep(50 * time.Millisecond)
	// Still running: a pass here must not read it as started by hand.
	handler.superviseOnce(context.Background())
	engine.wg.Wait()
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 0 || !handler.restarts.isStopped("paused") {
		t.Fatalf("started %v, recorded %v: the pause was undone", started, handler.restarts.isStopped("paused"))
	}
}

// With no answer at all, a tenant still running once the engine has had its time was never stopped.
func TestAStopWithNoAnswerExpires(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"x": tenant("running"), testFirewall: firewall("running", "healthy")})
	handler.restarts.stopGrace = time.Millisecond
	req := httptest.NewRequest(http.MethodPost, "/containers/x/stop", nil)
	if err := handler.beforeStop(req, "x"); err != nil {
		t.Fatal(err)
	}
	handler.stopUnanswered(req.Context())
	time.Sleep(5 * time.Millisecond)
	handler.superviseOnce(context.Background())
	if handler.restarts.isStopped("x") {
		t.Fatal("a stop that never happened keeps the tenant on the record")
	}
	engine.containers["x"].status = "exited"
	handler.superviseOnce(context.Background())
	if len(engine.startedOnly()) != 1 {
		t.Fatalf("started %v", engine.startedOnly())
	}
}

// A stop recorded while a hung start holds the slots is not undone by the start queued behind it.
func TestAQueuedStartHonoursAStopRecordedMeanwhile(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"a": tenant("exited"), "b": tenant("exited"), testFirewall: firewall("running", "healthy")})
	engine.hang = map[string]bool{"a": true}
	handler.restarts.workers, handler.restarts.startTimeout = 1, 300*time.Millisecond
	done := make(chan struct{})
	go func() { handler.startAll(context.Background(), []string{"a", "b"}); close(done) }()
	time.Sleep(50 * time.Millisecond)
	if err := handler.restarts.setStopped("b", true); err != nil {
		t.Fatal(err)
	}
	<-done
	if started := engine.startedOnly(); len(started) != 0 {
		t.Fatalf("started %v after its pause", started)
	}
}

// The proxy restarted mid-stop: whatever is running on the record gets the engine's time before reading as started by hand.
func TestAProxyRestartMidStopKeepsTheRecord(t *testing.T) {
	engine := &restartEngine{containers: map[string]*restartContainer{"x": tenant("running"), testFirewall: firewall("running", "healthy")}}
	upstream := httptest.NewServer(engine)
	t.Cleanup(upstream.Close)
	file := filepath.Join(t.TempDir(), "stopped")
	if err := os.WriteFile(file, []byte(stoppedHeader+"\nx\n"+stoppedEnd+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := guarded(t, upstream.URL, file)
	handler.superviseOnce(context.Background())
	engine.mu.Lock()
	engine.containers["x"].status = "exited"
	engine.mu.Unlock()
	handler.superviseOnce(context.Background())
	if started := engine.startedOnly(); len(started) != 0 {
		t.Fatalf("started %v: the stop in flight read as a crash", started)
	}
}

// The list predates the stop's end: it is read again before the record goes.
func TestAStaleListDoesNotReadAsStartedByHand(t *testing.T) {
	engine, handler, _ := restartProxy(t, map[string]*restartContainer{"x": tenant("running"), testFirewall: firewall("running", "healthy")})
	if err := handler.restarts.setStopped("x", true); err != nil {
		t.Fatal(err)
	}
	handler.restarts.stopping = map[string]time.Time{}
	engine.onList = func() { engine.containers["x"].status = "exited"; engine.onList = nil }
	handler.superviseOnce(context.Background())
	if !handler.restarts.isStopped("x") {
		t.Fatal("the record went on a list older than the stop")
	}
}

type slowStopEngine struct {
	*restartEngine
	stopTakes time.Duration
	wg        sync.WaitGroup
}

func (e *slowStopEngine) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/stop") {
		e.restartEngine.ServeHTTP(w, r)
		return
	}
	id := strings.Split(strings.TrimPrefix(r.URL.Path, "/containers/"), "/")[0]
	e.wg.Add(1)
	go func() { // as dockerd does: the stop goes on after the client hung up
		defer e.wg.Done()
		time.Sleep(e.stopTakes)
		e.mu.Lock()
		e.containers[id].status = "exited"
		e.mu.Unlock()
	}()
	select {
	case <-r.Context().Done():
	case <-time.After(e.stopTakes):
		w.WriteHeader(http.StatusNoContent)
	}
}

// A paused project's Pause does nothing while its row says PAUSED: the way out has to say Resume first.
func TestTheCorruptRecordLogGivesAWayToStopAPausedProjectAgain(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	guard := &restartGuard{Restarts: Restarts{StoppedFile: "/var/lib/excalibase-bridges/stopped"}}
	guard.logCorrupt()
	for _, want := range []string{"Resume", "then Pause", "cancel the deletion first"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("log %q does not say %q", logged.String(), want)
		}
	}
}
