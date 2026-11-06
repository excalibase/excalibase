package engineproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/logsafe"
)

// A reboot clears the host firewall's rules (EXC-683), and the engine starts a
// container with a restart policy (on-failure too) as soon as it is up, compose's
// dependencies unread. So a tenant on a guarded network has no policy, and the
// proxy does the policy's work: it starts what exited, except what was stopped
// through it, once the firewall is healthy, and refuses any start before that.
// It holds the socket and needs neither the platform database nor the vault.
// A stop from the engine's own CLI looks like a crash here, and such a tenant comes back.
//
// An app with a route out (APP_EGRESS=internet) or a host address (Docker before 28) is
// held the same way, since a project bridge is guarded only once the firewall's wildcard
// is back (EXC-687). On an internal network with an isolated gateway an app can reach
// nothing with or without the firewall, so the engine goes on restarting it.

// componentLabel marks what is not a project's long-running database or watcher:
// app replicas, and one-shot build and disk steps, which are not restarted.
const componentLabel = "excalibase.component"

// componentApp is an app replica.
const componentApp = "app"

// appRestartPolicy is what provisioning asks for an app, and what the engine does again once it may.
const appRestartPolicy = "unless-stopped"

// networkNone is the mode of a container with no network at all, which the firewall has nothing to guard from.
const networkNone = "none"

const (
	// superviseEvery bounds how long a crashed tenant stays down, and paces a crash loop.
	superviseEvery = 5 * time.Second
	// superviseStopGrace is how long a stop with no answer may still be going on in the engine:
	// provisioning's stops give a container 30 seconds.
	superviseStopGrace = 2 * time.Minute
	// corruptLogEvery repeats the way out of an unreadable record.
	corruptLogEvery = 5 * time.Minute
	// superviseStartTimeout and superviseWorkers keep a hung start from holding the other tenants' back.
	superviseStartTimeout = 30 * time.Second
	superviseWorkers      = 4
	// maxListBody bounds a container list: app containers carry long labels.
	maxListBody = 16 << 20
)

// errFirewallDown is the proxy's refusal; a client tells it apart by the 503 and this wording.
var errFirewallDown = fmt.Errorf("%w: the host firewall is not healthy", ErrRefused)

// errBridgeUnfenced: an older release's project network, until provisioning's reconcile remakes it.
var errBridgeUnfenced = fmt.Errorf("%w: a project network has no bridge the host firewall guards", ErrRefused)

// stopAttempt travels with a forwarded stop, for what its answer means to the record.
type stopAttempt struct {
	id     string
	marked bool
}

type stopAttemptKey struct{}

// WithRestarts guards tenant restarts, reading what an earlier run recorded as stopped.
func (h *Handler) WithRestarts(restarts Restarts) (*Handler, error) {
	guard, err := newRestartGuard(restarts)
	if err != nil {
		return nil, err
	}
	h.restarts = guard
	h.forward.ModifyResponse = func(resp *http.Response) error {
		h.stopAnswered(resp.Request.Context(), resp.StatusCode < http.StatusBadRequest)
		return h.addRestarts(resp)
	}
	h.forward.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.stopUnanswered(r.Context())
		log.Printf("engine proxy: forward %s: %v", logsafe.Value(r.URL.Path), err)
		w.WriteHeader(http.StatusBadGateway)
	}
	return h, nil
}

// stopAnswered ends a stop in flight; one the engine failed leaves a running
// tenant, which a record would keep down after its next crash.
func (h *Handler) stopAnswered(ctx context.Context, stopped bool) {
	attempt, ok := ctx.Value(stopAttemptKey{}).(stopAttempt)
	if !ok {
		return
	}
	h.restarts.endStopping(attempt.id)
	if stopped || !attempt.marked {
		return
	}
	if err := h.restarts.setStopped(attempt.id, false); err != nil {
		log.Printf("engine proxy: restarts: %v", err)
	}
}

// stopUnanswered is a stop with no answer, the caller gone: the engine finishes
// it anyway, so the record stands, and a tenant still running after the grace
// never stopped (settleStartedByHand).
func (h *Handler) stopUnanswered(ctx context.Context) {
	if attempt, ok := ctx.Value(stopAttemptKey{}).(stopAttempt); ok {
		h.restarts.expireStopping(attempt.id)
	}
}

// restartExempt: only on project networks the firewall has nothing to guard, or on none
// at all, is a container off the firewall's ground.
func restartExempt(p Policy, mode string, endpoints []string, nothingToGuard func(string) bool) bool {
	for _, network := range append([]string{mode}, endpoints...) {
		if network != networkNone && !(p.underNetworkPrefix(network) && nothingToGuard(network)) {
			return false
		}
	}
	return true
}

// fenceReading is what the engine says of a project network.
type fenceReading struct{ nothingToGuard, fenced bool }

// fenceReader reads project networks at most once per reading. A network it cannot read
// counts as needing the firewall, with a bridge no rule covers.
func (h *Handler) fenceReader(ctx context.Context) func(string) fenceReading {
	read := map[string]fenceReading{}
	return func(name string) fenceReading {
		if found, ok := read[name]; ok {
			return found
		}
		found := h.readFence(ctx, name)
		read[name] = found
		return found
	}
}

func (h *Handler) readFence(ctx context.Context, name string) fenceReading {
	if name == h.policy.NetworkPrefix+BuildEgressNetwork {
		return fenceReading{nothingToGuard: true}
	}
	var network bridgeNetwork
	if err := h.inspect(ctx, "/networks/"+url.PathEscape(name), &network); err != nil {
		return fenceReading{}
	}
	if network.Internal && network.Options[gatewayModeIPv4] == gatewayIsolated && network.Options[gatewayModeIPv6] == gatewayIsolated && !network.hasGateway() {
		return fenceReading{nothingToGuard: true}
	}
	return fenceReading{fenced: network.Options[bridgeNameOption] == ProjectBridgeName(name)}
}

// hasGateway: an address on the host, whatever was asked for (Docker 26 ignores the option).
func (n bridgeNetwork) hasGateway() bool {
	return slices.ContainsFunc(n.IPAM.Config, func(config struct{ Subnet, Gateway string }) bool { return config.Gateway != "" })
}

// nothingToGuard adapts a reader to restartExempt.
func nothingToGuard(read func(string) fenceReading) func(string) bool {
	return func(name string) bool { return read(name).nothingToGuard }
}

// unfenced names a project network with a bridge the firewall's wildcard does not cover.
func (h *Handler) unfenced(read func(string) fenceReading, networks ...string) string {
	for _, network := range networks {
		if !h.policy.underNetworkPrefix(network) {
			continue
		}
		if found := read(network); !found.nothingToGuard && !found.fenced {
			return network
		}
	}
	return ""
}

// holdRestartPolicy sends a guarded create to the engine with no policy to act on at boot.
func (h *Handler) holdRestartPolicy(r *http.Request, body []byte) error {
	if h.restarts == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber() // Memory and the like stay exact.
	var create map[string]any
	if err := decoder.Decode(&create); err != nil {
		return refuse("container create body: %v", err)
	}
	host, _ := create["HostConfig"].(map[string]any)
	if host == nil {
		return refuse("container create must send a HostConfig")
	}
	mode, _ := host["NetworkMode"].(string)
	var endpoints []string
	if networking, ok := create["NetworkingConfig"].(map[string]any); ok {
		if configs, ok := networking["EndpointsConfig"].(map[string]any); ok {
			for network := range configs {
				endpoints = append(endpoints, network)
			}
		}
	}
	if restartExempt(h.policy, mode, endpoints, nothingToGuard(h.fenceReader(r.Context()))) {
		return nil
	}
	host["RestartPolicy"] = map[string]any{"Name": "no", "MaximumRetryCount": 0}
	var held bytes.Buffer
	encoder := json.NewEncoder(&held)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(create); err != nil {
		return fmt.Errorf("encode the checked body: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(held.Bytes()))
	r.ContentLength = int64(held.Len())
	return nil
}

// guardedContainer is what the guard reads of one container.
type guardedContainer struct {
	ID     string `json:"Id"`
	Config struct{ Labels map[string]string }
	State  struct{ Status string }
	Host   struct {
		NetworkMode   string
		RestartPolicy struct{ Name string }
	} `json:"HostConfig"`
	Network struct{ Networks map[string]any } `json:"NetworkSettings"`
}

// networks names what the container is really attached to.
func (c guardedContainer) networks() []string {
	names := make([]string, 0, len(c.Network.Networks))
	for name := range c.Network.Networks {
		names = append(names, name)
	}
	return names
}

func (h *Handler) inspectGuarded(ctx context.Context, id string) (guardedContainer, error) {
	var found guardedContainer
	err := h.inspect(ctx, "/containers/"+url.PathEscape(id)+"/json", &found)
	return found, err
}

// beforeStart ends a stop, and refuses a guarded start until the firewall is healthy.
func (h *Handler) beforeStart(ctx context.Context, id string) error {
	found, err := h.inspectGuarded(ctx, id)
	if err != nil {
		return err
	}
	read := h.fenceReader(ctx)
	if restartExempt(h.policy, found.Host.NetworkMode, found.networks(), nothingToGuard(read)) {
		return nil
	}
	// First: refused now, the start is still wanted, and the supervisor does it once the firewall is healthy.
	if err := h.restarts.setStopped(found.ID, false); err != nil {
		return err
	}
	// As the engine zeroes its own on a start through the API: a resumed app gets its fresh attempts.
	h.restarts.clearRestarts(found.ID)
	if !h.firewallHealthy(ctx) {
		if found.State.Status == "created" {
			h.restarts.want(found.ID)
		}
		return fmt.Errorf("%w: tenants wait for it (docker logs %s)", errFirewallDown, h.restarts.FirewallContainer)
	}
	if network := h.unfenced(read, append(found.networks(), found.Host.NetworkMode)...); network != "" {
		if found.State.Status == "created" {
			h.restarts.want(found.ID)
		}
		return fmt.Errorf("%w: %s waits for provisioning to remake it", errBridgeUnfenced, network)
	}
	return nil
}

// beforeStop records the stop before the container exits, so it never reads as
// a crash; the answer to the stop ends it, and an error undoes the record.
func (h *Handler) beforeStop(r *http.Request, id string) error {
	found, err := h.inspectGuarded(r.Context(), id)
	if err != nil {
		return err
	}
	if !supervised(h.policy, found.Config.Labels, found.Host.NetworkMode, nothingToGuard(h.fenceReader(r.Context()))) {
		return nil
	}
	marked := !h.restarts.isStopped(found.ID)
	h.restarts.beginStopping(found.ID)
	if err := h.restarts.setStopped(found.ID, true); err != nil {
		h.restarts.endStopping(found.ID)
		return err
	}
	*r = *r.WithContext(context.WithValue(r.Context(), stopAttemptKey{}, stopAttempt{id: found.ID, marked: marked}))
	return nil
}

// supervised is a project's own long-running container on the firewall's ground; one-shot steps are not.
func supervised(p Policy, labels map[string]string, mode string, guardless func(string) bool) bool {
	if component := labels[componentLabel]; component != "" && component != componentApp {
		return false
	}
	return labels[p.ManagedLabel] == "true" && !restartExempt(p, mode, nil, guardless)
}

func (h *Handler) firewallHealthy(ctx context.Context) bool {
	var state platformState
	if err := h.inspect(ctx, "/containers/"+url.PathEscape(h.restarts.FirewallContainer)+"/json", &state); err != nil {
		return false
	}
	return state.State.Running && state.State.Health != nil && state.State.Health.Status == "healthy"
}

// EngineRootless asks the engine whether it runs rootless.
func (h *Handler) EngineRootless(ctx context.Context) (bool, error) {
	var info struct{ SecurityOptions []string }
	if err := h.inspect(ctx, "/info", &info); err != nil {
		return false, err
	}
	return slices.Contains(info.SecurityOptions, "name=rootless"), nil
}

// SuperviseRestarts keeps tenants running as a restart policy would, behind the firewall.
func (h *Handler) SuperviseRestarts(ctx context.Context) {
	for {
		h.superviseOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(superviseEvery):
		}
	}
}

type listedContainer struct {
	ID      string `json:"Id"`
	Labels  map[string]string
	State   string
	Host    struct{ NetworkMode string }      `json:"HostConfig"`
	Network struct{ Networks map[string]any } `json:"NetworkSettings"`
}

// attached is the mode and what the container is really on: a database's default route is on its project's bridge.
func (l listedContainer) attached() []string {
	names := make([]string, 0, len(l.Network.Networks)+1)
	names = append(names, l.Host.NetworkMode)
	for name := range l.Network.Networks {
		names = append(names, name)
	}
	return names
}

func (h *Handler) superviseOnce(ctx context.Context) {
	guard := h.restarts
	read := h.fenceReader(ctx)
	tenants, unsupervisedApps, err := h.listTenants(ctx, read)
	if err != nil {
		log.Printf("engine proxy: restarts: %v", err)
		return
	}
	for _, id := range unsupervisedApps {
		if err := h.handBack(ctx, id); err != nil {
			log.Printf("engine proxy: restarts: hand %s back to the engine: %v", id, err)
		}
	}
	// Before the health check: the policy of an older tenant is what a reboot acts on.
	for _, listed := range tenants {
		if err := h.adopt(ctx, listed.ID); err != nil {
			log.Printf("engine proxy: restarts: take over %s: %v", listed.ID, err)
		}
	}
	if guard.corrupt {
		guard.logCorrupt()
		return
	}
	h.settleStartedByHand(ctx, tenants)
	if !h.noteFirewall(h.firewallHealthy(ctx)) {
		return
	}
	h.startAll(ctx, h.toStart(tenants, read))
}

// logCorrupt says what this mode means and the way out, now and then.
func (g *restartGuard) logCorrupt() {
	if time.Since(g.corruptLogged) < corruptLogEvery {
		return
	}
	g.corruptLogged = time.Now()
	log.Printf("engine proxy: restarts: %[1]s cannot be read. It may name tenants stopped on purpose, so none is started on its own and a crashed one stays down. "+
		"To recover: docker exec excalibase-engine-proxy rm %[1]s, then docker restart excalibase-engine-proxy. "+
		"Every exited tenant then starts, paused projects' too: pause those again", g.StoppedFile)
}

// listTenants is the supervised containers and the apps left to the engine, after the
// records drop what the engine no longer holds.
func (h *Handler) listTenants(ctx context.Context, read func(string) fenceReading) ([]listedContainer, []string, error) {
	list, err := h.listManaged(ctx)
	if err != nil {
		return nil, nil, err
	}
	held := make(map[string]bool, len(list))
	tenants := make([]listedContainer, 0, len(list))
	var unsupervisedApps []string
	for _, listed := range list {
		held[listed.ID] = true
		switch {
		case supervised(h.policy, listed.Labels, listed.Host.NetworkMode, nothingToGuard(read)):
			tenants = append(tenants, listed)
		case listed.Labels[componentLabel] == componentApp:
			unsupervisedApps = append(unsupervisedApps, listed.ID)
		}
	}
	if err := h.restarts.forget(held); err != nil {
		log.Printf("engine proxy: restarts: %v", err)
	}
	return tenants, unsupervisedApps, nil
}

// settleStartedByHand: running with its stop answered, a tenant was started some
// other way (docker start), and is looked after again. Read again before acting:
// the list may predate the stop.
func (h *Handler) settleStartedByHand(ctx context.Context, tenants []listedContainer) {
	guard := h.restarts
	for _, listed := range tenants {
		if listed.State != "running" || !guard.isStopped(listed.ID) || guard.isStopping(listed.ID) {
			continue
		}
		found, err := h.inspectGuarded(ctx, listed.ID)
		if err != nil || found.State.Status != "running" || guard.isStopping(listed.ID) {
			continue
		}
		if err := guard.setStopped(listed.ID, false); err != nil {
			log.Printf("engine proxy: restarts: %v", err)
		}
	}
}

// noteFirewall logs the changes and answers healthy.
func (h *Handler) noteFirewall(healthy bool) bool {
	guard := h.restarts
	if guard.waiting == healthy {
		guard.waiting = !healthy
		if healthy {
			log.Printf("engine proxy: restarts: the host firewall is healthy, tenants may start")
		} else {
			log.Printf("engine proxy: restarts: tenants wait, the host firewall %s is not healthy", guard.FirewallContainer)
		}
	}
	return healthy
}

// startTarget is one container to start; crashedApp counts as a restart of an app.
type startTarget struct {
	id         string
	crashedApp bool
}

// toStart is what exited on its own and what a refused start left wanted, on a guarded bridge.
func (h *Handler) toStart(tenants []listedContainer, read func(string) fenceReading) []startTarget {
	guard := h.restarts
	var targets []startTarget
	for _, listed := range tenants {
		if h.unfenced(read, listed.attached()...) != "" {
			continue
		}
		switch {
		case listed.State == "exited" && !guard.isStopped(listed.ID):
			targets = append(targets, startTarget{id: listed.ID, crashedApp: listed.Labels[componentLabel] == componentApp})
		case listed.State == "created" && guard.wanted(listed.ID):
			targets = append(targets, startTarget{id: listed.ID})
		}
	}
	return targets
}

// startAll starts them a few at a time, each bounded and behind a firewall checked just before.
func (h *Handler) startAll(ctx context.Context, targets []startTarget) {
	guard := h.restarts
	slots := make(chan struct{}, guard.workers)
	var running sync.WaitGroup
	for _, target := range targets {
		id := target.id
		slots <- struct{}{}
		running.Add(1)
		go func() {
			defer func() { <-slots; running.Done() }()
			// Read again: a slot may have taken a while, and a pause or the firewall gone meanwhile counts.
			if guard.isStopped(id) || !h.firewallHealthy(ctx) {
				return
			}
			started, cancel := context.WithTimeout(ctx, guard.startTimeout)
			defer cancel()
			if err := h.engineCall(started, "/containers/"+url.PathEscape(id)+"/start", nil); err != nil {
				log.Printf("engine proxy: restarts: start %s: %v", id, err)
				return
			}
			guard.unwant(id)
			if target.crashedApp {
				guard.noteRestart(id)
			}
			log.Printf("engine proxy: restarts: started %s", id)
		}()
	}
	running.Wait()
}

// adopt takes an older release's tenant off the engine's policy. Stopped under
// that policy, it was stopped on purpose: the engine would have restarted it otherwise.
func (h *Handler) adopt(ctx context.Context, id string) error {
	guard := h.restarts
	if guard.isAdopted(id) {
		return nil
	}
	found, err := h.inspectGuarded(ctx, id)
	if err != nil {
		return err
	}
	if name := found.Host.RestartPolicy.Name; name != "" && name != "no" {
		if found.State.Status == "exited" {
			if err := guard.setStopped(id, true); err != nil {
				return err
			}
		}
		if err := h.engineCall(ctx, "/containers/"+url.PathEscape(id)+"/update", []byte(`{"RestartPolicy":{"Name":"no"}}`)); err != nil {
			return err
		}
		log.Printf("engine proxy: restarts: took %s off the engine's %s policy", id, name)
	}
	guard.markAdopted(id)
	guard.setIn(guard.handedBack, id, false)
	return nil
}

// handBack returns an app to the engine's policy once its network needs no firewall
// (APP_EGRESS back to none), and starts what crashed while none looked after it.
func (h *Handler) handBack(ctx context.Context, id string) error {
	guard := h.restarts
	if guard.holds(guard.handedBack, id) {
		return nil
	}
	found, err := h.inspectGuarded(ctx, id)
	if err != nil {
		return err
	}
	if found.Host.RestartPolicy.Name == "no" {
		if err := h.engineCall(ctx, "/containers/"+url.PathEscape(id)+"/update", []byte(`{"RestartPolicy":{"Name":"`+appRestartPolicy+`"}}`)); err != nil {
			return err
		}
		log.Printf("engine proxy: restarts: gave %s back to the engine's %s policy", id, appRestartPolicy)
		if found.State.Status == "exited" && !guard.isStopped(id) {
			if err := h.engineCall(ctx, "/containers/"+url.PathEscape(id)+"/start", nil); err != nil {
				return err
			}
		}
	}
	guard.setIn(guard.handedBack, id, true)
	guard.setIn(guard.adopted, id, false)
	return nil
}

// addRestarts adds the restarts the proxy did to an inspect's RestartCount: only the
// engine's own policy raises it, and rollouts and Studio read it. Kept for this process's life.
func (h *Handler) addRestarts(resp *http.Response) error {
	guard := h.restarts
	if resp.Request.Method != http.MethodGet || resp.StatusCode != http.StatusOK || !guard.anyRestarts() {
		return nil
	}
	match := containerPath.FindStringSubmatch(stripVersion(resp.Request.URL.Path))
	if match == nil || match[2] != "/json" {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxListBody))
	if err != nil {
		return fmt.Errorf("read an inspect: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("read an inspect: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var inspected map[string]any
	if err := decoder.Decode(&inspected); err != nil {
		return fmt.Errorf("read an inspect: %w", err)
	}
	id, _ := inspected["Id"].(string)
	if done := guard.restartsOf(id); done > 0 {
		var engine int64
		if count, ok := inspected["RestartCount"].(json.Number); ok {
			engine, _ = count.Int64()
		}
		inspected["RestartCount"] = engine + done
		if body, err = json.Marshal(inspected); err != nil {
			return fmt.Errorf("write an inspect: %w", err)
		}
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}

func (h *Handler) listManaged(ctx context.Context) ([]listedContainer, error) {
	filter, err := json.Marshal(map[string][]string{"label": {h.policy.ManagedLabel + "=true"}})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	target := *h.target
	target.Path = "/containers/json"
	target.RawQuery = url.Values{"all": {"true"}, "filters": {string(filter)}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.transport.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list containers: engine answered %d", resp.StatusCode)
	}
	var list []listedContainer
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxListBody)).Decode(&list); err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	return list, nil
}

// engineCall POSTs to the engine; a start of what already runs (304) is no failure.
func (h *Handler) engineCall(ctx context.Context, apiPath string, body []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	target := *h.target
	target.Path = apiPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.transport.RoundTrip(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("engine answered %d: %s", resp.StatusCode, logsafe.Value(string(detail)))
	}
	return nil
}
