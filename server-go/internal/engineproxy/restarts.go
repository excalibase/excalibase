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

// networkNone is the mode of a container with no network at all, which the firewall has nothing to guard from.
const networkNone = "none"

const (
	// superviseEvery bounds how long a crashed tenant stays down, and paces a crash loop.
	superviseEvery = 5 * time.Second
	// superviseStartTimeout and superviseWorkers keep a hung start from holding the other tenants' back.
	superviseStartTimeout = 30 * time.Second
	superviseWorkers      = 4
	// maxListBody bounds a container list: app containers carry long labels.
	maxListBody = 16 << 20
)

// errFirewallDown is the proxy's refusal; a client tells it apart by the 503 and this wording.
var errFirewallDown = fmt.Errorf("%w: the host firewall is not healthy", ErrRefused)

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
		return nil
	}
	h.forward.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		h.stopAnswered(r.Context(), false)
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

// restartExempt: only on project networks alone, or on none at all, is a container off the firewall's ground.
func restartExempt(p Policy, mode string, endpoints []string) bool {
	if mode != networkNone && !p.underNetworkPrefix(mode) {
		return false
	}
	for _, network := range endpoints {
		if network != networkNone && !p.underNetworkPrefix(network) {
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
		if found.State.Status == "created" {
			h.restarts.want(found.ID)
		}
		return fmt.Errorf("%w: tenants wait for it (docker logs %s)", errFirewallDown, h.restarts.FirewallContainer)
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
	if !supervised(h.policy, found.Config.Labels, found.Host.NetworkMode) {
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

// supervised is a project's own container on the firewall's ground; apps and one-shot steps are not.
func supervised(p Policy, labels map[string]string, mode string) bool {
	return labels[p.ManagedLabel] == "true" && labels[componentLabel] == "" && !restartExempt(p, mode, nil)
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
	ID     string `json:"Id"`
	Labels map[string]string
	State  string
	Host   struct{ NetworkMode string } `json:"HostConfig"`
}

func (h *Handler) superviseOnce(ctx context.Context) {
	guard := h.restarts
	tenants, err := h.listTenants(ctx)
	if err != nil {
		log.Printf("engine proxy: restarts: %v", err)
		return
	}
	// Before the health check: the policy of an older tenant is what a reboot acts on.
	for _, listed := range tenants {
		if err := h.adopt(ctx, listed.ID); err != nil {
			log.Printf("engine proxy: restarts: take over %s: %v", listed.ID, err)
		}
	}
	if guard.corrupt {
		log.Printf("engine proxy: restarts: %s cannot be read, so no tenant is started: it may name ones stopped on purpose. "+
			"Start them through the platform, or remove the file and restart this container to start them all", guard.StoppedFile)
		return
	}
	h.settleStartedByHand(ctx, tenants)
	if !h.noteFirewall(h.firewallHealthy(ctx)) {
		return
	}
	h.startAll(ctx, h.toStart(tenants))
}

// listTenants is the supervised containers, after the records drop what the engine no longer holds.
func (h *Handler) listTenants(ctx context.Context) ([]listedContainer, error) {
	list, err := h.listManaged(ctx)
	if err != nil {
		return nil, err
	}
	held := make(map[string]bool, len(list))
	tenants := make([]listedContainer, 0, len(list))
	for _, listed := range list {
		held[listed.ID] = true
		if supervised(h.policy, listed.Labels, listed.Host.NetworkMode) {
			tenants = append(tenants, listed)
		}
	}
	if err := h.restarts.forget(held); err != nil {
		log.Printf("engine proxy: restarts: %v", err)
	}
	return tenants, nil
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

// toStart is what exited on its own, and what a refused start left wanted.
func (h *Handler) toStart(tenants []listedContainer) []string {
	guard := h.restarts
	var ids []string
	for _, listed := range tenants {
		switch {
		case listed.State == "exited" && !guard.isStopped(listed.ID):
			ids = append(ids, listed.ID)
		case listed.State == "created" && guard.wanted(listed.ID):
			ids = append(ids, listed.ID)
		}
	}
	return ids
}

// startAll starts them a few at a time, each bounded and behind a firewall checked just before.
func (h *Handler) startAll(ctx context.Context, ids []string) {
	guard := h.restarts
	slots := make(chan struct{}, guard.workers)
	var running sync.WaitGroup
	for _, id := range ids {
		slots <- struct{}{}
		running.Add(1)
		go func() {
			defer func() { <-slots; running.Done() }()
			if !h.firewallHealthy(ctx) {
				return
			}
			started, cancel := context.WithTimeout(ctx, guard.startTimeout)
			defer cancel()
			if err := h.engineCall(started, "/containers/"+url.PathEscape(id)+"/start", nil); err != nil {
				log.Printf("engine proxy: restarts: start %s: %v", id, err)
				return
			}
			guard.unwant(id)
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
		// With the record unreadable this adds no entry, and nothing is started anyway.
		if found.State.Status == "exited" && !guard.corrupt {
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
