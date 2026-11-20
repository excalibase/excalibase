package dockerapps

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"slices"
	"strings"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"

	"github.com/excalibase/provisioning-poc/internal/engineproxy"
)

// An internal bridge still has an address on the host, and every host service
// listening on all addresses answers there (EXC-676). Docker 28's isolated
// gateway leaves the bridge without one; containers on it still reach each other.
// The IPv6 mode matters where the daemon enables IPv6 on every network by default.
const (
	gatewayModeIPv4 = "com.docker.network.bridge.gateway_mode_ipv4"
	gatewayModeIPv6 = "com.docker.network.bridge.gateway_mode_ipv6"
	gatewayIsolated = "isolated"
)

// IsolatedGatewayOptions give an internal bridge no address on the host, in both address families (Docker 28+).
// Site builds (EXC-671) make their networks with them too.
func IsolatedGatewayOptions() map[string]string {
	return map[string]string{gatewayModeIPv4: gatewayIsolated, gatewayModeIPv6: gatewayIsolated}
}

// bridgeNameOption gives a bridge its interface name on the host.
const bridgeNameOption = "com.docker.network.bridge.name"

// FencedBridgeOptions name a Docker bridge with a route or a host address under the host
// firewall's wildcard, whose rule is in place before the engine makes it (EXC-687).
func FencedBridgeOptions(network string) map[string]string {
	return map[string]string{bridgeNameOption: engineproxy.ProjectBridgeName(network)}
}

// GatewayModeRefused is Docker 27's 400: it knows the option but not the isolated mode.
func GatewayModeRefused(err error) bool {
	return errdefs.IsInvalidArgument(err) && strings.Contains(err.Error(), "gateway mode")
}

// hostFirewall is the fix where the engine cannot isolate: rootful Podman's resolver listens on the bridge.
const hostFirewall = "add a host firewall rule dropping new connections from container bridges to the host " +
	"(iptables -I INPUT -i br-+ -m conntrack ! --ctstate ESTABLISHED,RELATED -j DROP; " +
	"rootful Podman: the same for podman+, after accepting port 53 there)"

// hostIsolation is what VerifyEngine found about project networks and the host.
type hostIsolation struct {
	// gateway: the engine honours the isolated gateway, so internal project networks are made with it.
	gateway bool
	// isolated: apps reach no service of the host; why says what to do when not.
	isolated bool
	why      string
}

func (r *Runtime) setHostIsolation(found hostIsolation) {
	r.isolationMu.Lock()
	defer r.isolationMu.Unlock()
	r.isolation = found
}

func (r *Runtime) hostIsolation() hostIsolation {
	r.isolationMu.Lock()
	defer r.isolationMu.Unlock()
	return r.isolation
}

// HostIsolation is whether apps can reach no service of the host, and why not. Valid after VerifyEngine.
func (r *Runtime) HostIsolation() (bool, string) {
	found := r.hostIsolation()
	return found.isolated, found.why
}

// isolatedGateway: internal project networks get no address on the host.
func (r *Runtime) isolatedGateway() bool {
	return !r.opts.Egress && r.hostIsolation().gateway
}

// makeCheckNetwork makes the startup check's network the way project networks
// will be made, and finds whether its bridge has an address on the host.
func (r *Runtime) makeCheckNetwork(ctx context.Context, name string) error {
	plain := network.CreateOptions{
		Driver: "bridge", Internal: true, Labels: map[string]string{labelManaged: "true", labelComponent: componentDiskTool},
	}
	if r.opts.Isolate {
		// Before anything is made: no firewall sees what pasta sends as this user's own connections.
		if err := r.refuseUnguardedEgress(ctx); err != nil {
			return err
		}
		if _, err := r.engine.NetworkCreate(ctx, name, plain); err != nil {
			return err
		}
		found, err := r.podmanIsolation(ctx)
		if err != nil {
			return err
		}
		r.setHostIsolation(found)
		return nil
	}
	isolated := plain
	isolated.Options = IsolatedGatewayOptions()
	_, err := r.engine.NetworkCreate(ctx, name, isolated)
	// Docker 27 knows the option but not the mode; any other refusal fails the start.
	if GatewayModeRefused(err) {
		plain.Options = FencedBridgeOptions(name)
		if _, plainErr := r.engine.NetworkCreate(ctx, name, plain); plainErr != nil {
			return plainErr
		}
		r.setHostIsolation(hostIsolation{isolated: r.opts.HostFirewall, why: fmt.Sprintf("the engine refused an isolated gateway (%v): "+
			"apps can reach host services listening on every address; upgrade to Docker 28 or later, or %s", err, hostFirewall)})
		return nil
	}
	if err != nil {
		return err
	}
	inspect, err := r.engine.NetworkInspect(ctx, name, network.InspectOptions{})
	if err != nil {
		return fmt.Errorf("read the check network: %w", err)
	}
	if hasHostAddress(inspect) {
		r.setHostIsolation(hostIsolation{isolated: r.opts.HostFirewall, why: "the engine gave an isolated network an address on the host anyway: " +
			"apps can reach host services listening on every address; upgrade to Docker 28 or later, or " + hostFirewall})
		return nil
	}
	found := hostIsolation{gateway: true, isolated: !r.opts.Egress || r.opts.HostFirewall}
	if !found.isolated {
		found.why = "APP_EGRESS=internet with the host firewall off (HOST_FIREWALL=off): apps and their databases can reach cloud " +
			"metadata, private ranges and the host's services unless you guard the bridges " + engineproxy.ProjectBridgePrefix + "+ yourself"
	}
	r.setHostIsolation(found)
	return nil
}

// podmanIsolation: netavark refuses Docker's gateway mode. Rootless, a bridge's address is in the
// rootless network namespace, not the host's; with egress, pasta maps host.containers.internal to the host.
func (r *Runtime) podmanIsolation(ctx context.Context) (hostIsolation, error) {
	info, err := r.engine.Info(ctx)
	if err != nil {
		return hostIsolation{}, fmt.Errorf("read whether Podman runs rootless: %w", err)
	}
	if !slices.Contains(info.SecurityOptions, "name=rootless") {
		return hostIsolation{why: "rootful Podman gives project networks an address on the host: apps can reach host " +
			"services listening on every address unless you " + hostFirewall}, nil
	}
	if r.opts.Egress {
		return hostIsolation{why: unguardedEgress}, nil
	}
	return hostIsolation{isolated: true}, nil
}

// unguardedEgress says what rootless Podman's egress leaves open and what it keeps closed.
const unguardedEgress = "APP_EGRESS=internet on rootless Podman: an app's traffic leaves through pasta as this user's own " +
	"connections, which no firewall of ours can see, so it reaches the LAN, cloud metadata, and the host's services on " +
	"host.containers.internal unless they listen on 127.0.0.1 alone. Other projects' bridges stay closed (netavark's isolate)"

// refuseUnguardedEgress: rootless Podman's egress takes an explicit yes. Rootful is not supported and stops at the proxy.
func (r *Runtime) refuseUnguardedEgress(ctx context.Context) error {
	if !r.opts.Egress || r.opts.AcceptUnguardedEgress {
		return nil
	}
	info, err := r.engine.Info(ctx)
	if err != nil {
		return fmt.Errorf("read whether Podman runs rootless: %w", err)
	}
	if !slices.Contains(info.SecurityOptions, "name=rootless") {
		return nil
	}
	return errors.New(unguardedEgress + ". Keep APP_EGRESS=none, use Docker, or set APP_EGRESS_UNGUARDED=accept to run it this way knowingly")
}

func hasHostAddress(inspect network.Inspect) bool {
	for _, config := range inspect.IPAM.Config {
		if config.Gateway != "" {
			return true
		}
	}
	return false
}

// olderNetwork is a project network made with an address on the host by an earlier
// release, or made with egress the operator has since turned off (or on).
func (r *Runtime) olderNetwork(inspect network.Inspect) bool {
	if inspect.Internal == r.opts.Egress {
		return true
	}
	if r.isolatedGateway() {
		return inspect.Options[gatewayModeIPv4] != gatewayIsolated || inspect.Options[gatewayModeIPv6] != gatewayIsolated
	}
	return !r.opts.Isolate && inspect.Options[bridgeNameOption] != engineproxy.ProjectBridgeName(inspect.Name)
}

// remakeProjectNetwork replaces an older project network: an engine cannot change a
// network's options, so its members are taken off, it is made again, and the caller
// puts them back. Running apps are not restarted; their connections reset once.
func (r *Runtime) remakeProjectNetwork(ctx context.Context, project, database string, older network.Inspect) (network.Inspect, error) {
	apps, err := r.projectApps(ctx, project)
	if err != nil {
		return network.Inspect{}, err
	}
	endpoints := appEndpoints(apps)
	members := r.memberRefs(older, append([]string{database}, slices.Collect(maps.Keys(endpoints))...)...)
	for _, member := range members {
		if err := r.engine.NetworkDisconnect(ctx, older.Name, member, true); err != nil && !notMember(err) {
			return network.Inspect{}, errors.Join(fmt.Errorf("take %s off the older project network: %w", member, err),
				r.restoreMembers(ctx, older, members, endpoints))
		}
	}
	if err := r.engine.NetworkRemove(ctx, older.Name); err != nil && !errdefs.IsNotFound(err) {
		return network.Inspect{}, errors.Join(fmt.Errorf("remove the older project network: %w", err),
			r.restoreMembers(ctx, older, members, endpoints))
	}
	// From here a failed pass leaves no network: the next one makes it and puts everyone back.
	if _, err := r.engine.NetworkCreate(ctx, older.Name, r.networkOptions(project)); err != nil && !errdefs.IsConflict(err) {
		return network.Inspect{}, fmt.Errorf("make the project's network again: %w", err)
	}
	log.Printf("INFO: app networks: remade %s as this release makes project networks; its apps keep running", older.Name)
	return r.engine.NetworkInspect(ctx, older.Name, network.InspectOptions{})
}

// restoreMembers puts members back on the older network when it could not be removed,
// so a failed remake leaves the project as it was rather than offline.
func (r *Runtime) restoreMembers(ctx context.Context, older network.Inspect, members []string, endpoints map[string]*network.EndpointSettings) error {
	var errs []error
	for _, member := range members {
		settings := endpoints[member]
		if member == r.opts.EdgeContainer {
			settings = &network.EndpointSettings{Aliases: slices.Clone(r.opts.EdgeAliases)}
		}
		if err := r.connect(ctx, older.Name, member, settings); err != nil && !errdefs.IsNotFound(err) {
			errs = append(errs, fmt.Errorf("put %s back on the older project network: %w", member, err))
		}
	}
	return errors.Join(errs...)
}

// memberRefs are a network's members plus extra, as the proxy names them: the edge by
// name only, every other member (managed) by id.
func (r *Runtime) memberRefs(inspect network.Inspect, extra ...string) []string {
	refs := make([]string, 0, len(extra)+len(inspect.Containers))
	add := func(ref string) {
		if ref != "" && !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	for _, ref := range extra {
		add(ref)
	}
	for id, member := range inspect.Containers {
		if member.Name == r.opts.EdgeContainer {
			add(member.Name)
		} else {
			add(id)
		}
	}
	return refs
}

// projectApps are the project's app containers, stopped ones too: a stopped container
// is not listed among a network's members.
func (r *Runtime) projectApps(ctx context.Context, project string) ([]container.Summary, error) {
	list, err := r.engine.ContainerList(ctx, container.ListOptions{All: true,
		Filters: labelFilter(labelComponent, componentApp, labelProject, project)})
	if err != nil {
		return nil, fmt.Errorf("list the project's apps: %w", err)
	}
	return list, nil
}

// appEndpoints are the settings each app joins its project's network under, by id: its one
// service name, which the project's other apps reach it by.
func appEndpoints(apps []container.Summary) map[string]*network.EndpointSettings {
	endpoints := make(map[string]*network.EndpointSettings, len(apps))
	for _, app := range apps {
		endpoints[app.ID] = nil
		if name := app.Labels[labelAppName]; name != "" {
			endpoints[app.ID] = &network.EndpointSettings{Aliases: []string{name + "." + engineproxy.AppServiceDomain}}
		}
	}
	return endpoints
}

// rejoinApps puts back any of the project's apps that are off its network: after a
// remake, or one interrupted at any step. A member already on it is left alone.
func (r *Runtime) rejoinApps(ctx context.Context, project string, inspect network.Inspect) error {
	apps, err := r.projectApps(ctx, project)
	if err != nil {
		return err
	}
	for id, settings := range appEndpoints(apps) {
		if err := r.joinAs(ctx, inspect, id, settings); err != nil && !errdefs.IsNotFound(err) {
			return fmt.Errorf("put app container %s back on the project's network: %w", id, err)
		}
	}
	return nil
}
