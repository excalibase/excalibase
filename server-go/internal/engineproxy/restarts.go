package engineproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
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
// An app's project network has no address on the host and no rule of the firewall's,
// so the engine goes on restarting apps; a stop from the engine's own CLI looks
// like a crash here, and such a tenant comes back.

// componentLabel marks what is not a project's long-running database or watcher:
// app replicas, which the engine restarts, and one-shot build and disk steps.
const componentLabel = "excalibase.component"

// superviseEvery bounds how long a crashed tenant stays down, and paces a crash loop.
const superviseEvery = 5 * time.Second

// maxListBody bounds a container list: app containers carry long labels.
const maxListBody = 16 << 20

// Restarts turns the guard on. Rootless Podman has no firewall to wait for and goes without.
type Restarts struct {
	// FirewallContainer must report healthy before a guarded container starts.
	FirewallContainer string
	// StoppedFile keeps the ids stopped on purpose across a reboot, one per line.
	StoppedFile string
}

type restartGuard struct {
	Restarts
	mu      sync.Mutex
	stopped map[string]bool
	// adopted is what this process has checked for an older release's policy.
	adopted map[string]bool
	waiting bool
}

// WithRestarts guards tenant restarts, reading what an earlier run recorded as stopped.
func (h *Handler) WithRestarts(restarts Restarts) (*Handler, error) {
	guard := &restartGuard{Restarts: restarts, stopped: map[string]bool{}, adopted: map[string]bool{}}
	raw, err := os.ReadFile(restarts.StoppedFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", restarts.StoppedFile, err)
	}
	for _, id := range strings.Fields(string(raw)) {
		guard.stopped[id] = true
	}
	h.restarts = guard
	return h, nil
}

// setStopped records a change first: a stop forgotten across a reboot would look like a crash.
func (g *restartGuard) setStopped(id string, stopped bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.stopped[id] == stopped {
		return nil
	}
	next := make(map[string]bool, len(g.stopped)+1)
	for held := range g.stopped {
		next[held] = true
	}
	if stopped {
		next[id] = true
	} else {
		delete(next, id)
	}
	if err := g.save(next); err != nil {
		return fmt.Errorf("record %s: %w", g.StoppedFile, err)
	}
	g.stopped = next
	return nil
}

func (g *restartGuard) save(stopped map[string]bool) error {
	ids := make([]string, 0, len(stopped))
	for id := range stopped {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return WriteBridges(g.StoppedFile, ids)
}

func (g *restartGuard) isStopped(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.stopped[id]
}

// forget drops what the engine no longer holds.
func (g *restartGuard) forget(held map[string]bool) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	for id := range g.adopted {
		if !held[id] {
			delete(g.adopted, id)
		}
	}
	next := make(map[string]bool, len(g.stopped))
	for id := range g.stopped {
		if held[id] {
			next[id] = true
		}
	}
	if len(next) == len(g.stopped) {
		return nil
	}
	if err := g.save(next); err != nil {
		return fmt.Errorf("record %s: %w", g.StoppedFile, err)
	}
	g.stopped = next
	return nil
}

// restartExempt: only on project networks alone is a container off the firewall's ground.
func restartExempt(p Policy, mode string, endpoints []string) bool {
	if !p.underNetworkPrefix(mode) {
		return false
	}
	for _, network := range endpoints {
		if !p.underNetworkPrefix(network) {
			return false
		}
	}
	return true
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
	if restartExempt(h.policy, mode, endpoints) {
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
	if restartExempt(h.policy, found.Host.NetworkMode, found.networks()) {
		return nil
	}
	// First: refused now, the start is still wanted, and the supervisor does it once the firewall is healthy.
	if err := h.restarts.setStopped(found.ID, false); err != nil {
		return err
	}
	if !h.firewallHealthy(ctx) {
		return refuse("the host firewall is not healthy: tenants wait for it (docker logs %s)", h.restarts.FirewallContainer)
	}
	return nil
}

// beforeStop records the stop before the container exits, so it never reads as a crash.
// A stop the engine then fails leaves the record: such a tenant waits for its next start.
func (h *Handler) beforeStop(ctx context.Context, id string) error {
	found, err := h.inspectGuarded(ctx, id)
	if err != nil {
		return err
	}
	if !supervised(h.policy, found.Config.Labels, found.Host.NetworkMode) {
		return nil
	}
	return h.restarts.setStopped(found.ID, true)
}

// supervised is a project's own container on the firewall's ground; apps and one-shot steps are not.
func supervised(p Policy, labels map[string]string, mode string) bool {
	return labels[p.ManagedLabel] == "true" && labels[componentLabel] == "" && !p.underNetworkPrefix(mode)
}

func (h *Handler) firewallHealthy(ctx context.Context) bool {
	var state platformState
	if err := h.inspect(ctx, "/containers/"+url.PathEscape(h.restarts.FirewallContainer)+"/json", &state); err != nil {
		return false
	}
	return state.State.Running && state.State.Health != nil && state.State.Health.Status == "healthy"
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
	ID     string `json:"Id"`
	Labels map[string]string
	State  string
	Host   struct{ NetworkMode string } `json:"HostConfig"`
}

func (h *Handler) superviseOnce(ctx context.Context) {
	guard := h.restarts
	list, err := h.listManaged(ctx)
	if err != nil {
		log.Printf("engine proxy: restarts: %v", err)
		return
	}
	held := make(map[string]bool, len(list))
	tenants := make([]listedContainer, 0, len(list))
	for _, listed := range list {
		held[listed.ID] = true
		if supervised(h.policy, listed.Labels, listed.Host.NetworkMode) {
			tenants = append(tenants, listed)
		}
	}
	if err := guard.forget(held); err != nil {
		log.Printf("engine proxy: restarts: %v", err)
	}
	// Before the health check: the policy of an older tenant is what a reboot acts on.
	for _, listed := range tenants {
		if err := h.adopt(ctx, listed.ID); err != nil {
			log.Printf("engine proxy: restarts: take over %s: %v", listed.ID, err)
		}
	}
	healthy := h.firewallHealthy(ctx)
	if guard.waiting == healthy {
		guard.waiting = !healthy
		if healthy {
			log.Printf("engine proxy: restarts: the host firewall is healthy, tenants may start")
		} else {
			log.Printf("engine proxy: restarts: tenants wait, the host firewall %s is not healthy", guard.FirewallContainer)
		}
	}
	if !healthy {
		return
	}
	for _, listed := range tenants {
		if listed.State != "exited" || guard.isStopped(listed.ID) {
			continue
		}
		if err := h.engineCall(ctx, "/containers/"+url.PathEscape(listed.ID)+"/start", nil); err != nil {
			log.Printf("engine proxy: restarts: start %s: %v", listed.ID, err)
			continue
		}
		log.Printf("engine proxy: restarts: started %s", listed.ID)
	}
}

// adopt takes an older release's tenant off the engine's policy. Stopped under
// that policy, it was stopped on purpose: the engine would have restarted it otherwise.
func (h *Handler) adopt(ctx context.Context, id string) error {
	guard := h.restarts
	guard.mu.Lock()
	done := guard.adopted[id]
	guard.mu.Unlock()
	if done {
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
	guard.mu.Lock()
	guard.adopted[id] = true
	guard.mu.Unlock()
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
