// Package engineproxy stands between provisioning and the Docker or Podman
// API socket. Holding the socket is root on a Docker host, so provisioning
// gets a proxy that forwards only the calls it makes, only against the
// containers it manages, and refuses any container that could reach the host.
package engineproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ManagedLabel is the label provisioning puts on every container it creates.
const ManagedLabel = "excalibase.managed"

// Policy is what a proxied container create may ask for.
type Policy struct {
	// ManagedLabel marks containers provisioning created; every container
	// call must target one, and every create must carry it as "true".
	ManagedLabel string
	// Networks are the networks a container may join by exact name.
	Networks []string
	// NetworkPrefix admits per-project networks; empty admits none.
	NetworkPrefix string
	// PortBindIPs are the host addresses a published port may bind; the host
	// port itself is always the engine's choice.
	PortBindIPs []string
	// VolumePrefix is the name every named volume must start with, so a
	// container cannot mount the platform's own volumes. Empty admits only
	// anonymous volumes.
	VolumePrefix string
	// Runtimes are the OCI runtimes a container may ask for besides the default.
	Runtimes []string
	// ContainerPrefix is the name every created container must start with,
	// so a create cannot take a platform service's name.
	ContainerPrefix string
	// EdgeContainer is the one unmanaged container that may join and leave
	// project networks, to route to their apps; empty admits none.
	EdgeContainer string
}

// ErrRefused wraps every policy refusal.
var ErrRefused = errors.New("refused by the engine proxy")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, args...))
}

// hostConfigCheck validates one HostConfig field's non-zero value.
type hostConfigCheck func(p Policy, value any) error

// hostConfigFields lists every HostConfig field a create may set. Anything
// else must be absent or zero, so a field added to the API later is refused
// until someone decides it is safe.
var hostConfigFields = map[string]hostConfigCheck{
	"PortBindings":      checkPortBindings,
	"RestartPolicy":     allow,
	"Memory":            allow,
	"MemoryReservation": allow,
	"MemorySwap":        allow,
	"MemorySwappiness":  allow,
	"NanoCpus":          allow,
	"CpuShares":         allow,
	"CpuQuota":          allow,
	"CpuPeriod":         allow,
	"PidsLimit":         allow,
	"Ulimits":           allow,
	"ShmSize":           allow,
	"CapDrop":           allow,
	"CapAdd":            checkCapAdd,
	"Init":              allow,
	"ReadonlyRootfs":    allow,
	"AutoRemove":        allow,
	"Tmpfs":             allow,
	"ConsoleSize":       allow,
	"Binds":             checkBinds,
	"Mounts":            checkMounts,
	"NetworkMode":       checkNetworkMode,
	"Runtime":           checkRuntime,
	"SecurityOpt":       checkSecurityOpt,
	"LogConfig":         checkLogConfig,
	"IpcMode":           privateOnly("IpcMode", "private", "none", "shareable"),
	"CgroupnsMode":      privateOnly("CgroupnsMode", "private"),
}

func allow(Policy, any) error { return nil }

// CheckCreate decides whether a POST /containers/create may reach the engine.
func (p Policy) CheckCreate(name string, body []byte) error {
	if !strings.HasPrefix(name, p.ContainerPrefix) || name == p.ContainerPrefix {
		return refuse("container name %q must start with %q", name, p.ContainerPrefix)
	}
	var create struct {
		Labels           map[string]string
		HostConfig       map[string]any
		NetworkingConfig struct {
			EndpointsConfig map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal(body, &create); err != nil {
		return refuse("container create body: %v", err)
	}
	if create.Labels[p.ManagedLabel] != "true" {
		return refuse("container must carry the label %s=true", p.ManagedLabel)
	}
	if create.HostConfig == nil {
		return refuse("container create must send a HostConfig")
	}
	for field, value := range create.HostConfig {
		if err := p.checkHostConfigField(field, value); err != nil {
			return err
		}
	}
	for network, endpoint := range create.NetworkingConfig.EndpointsConfig {
		if !p.networkAllowed(network) {
			return refuse("network %q is not one containers may join", network)
		}
		if err := checkEndpoint(network, endpoint); err != nil {
			return err
		}
	}
	return nil
}

// checkEndpoint refuses aliases, fixed addresses and the like: on a shared
// network an alias or a taken address answers for a platform service.
func checkEndpoint(network string, raw json.RawMessage) error {
	var endpoint any
	if err := json.Unmarshal(raw, &endpoint); err != nil {
		return refuse("endpoint on %q: %v", network, err)
	}
	if !isZero(endpoint) {
		return refuse("endpoint settings on %q may not be set (aliases, addresses, links)", network)
	}
	return nil
}

// unmaskingFields are refused whenever present: an empty list there is not
// "unset" but "unmask everything".
var unmaskingFields = []string{"MaskedPaths", "ReadonlyPaths"}

func (p Policy) checkHostConfigField(field string, value any) error {
	if value != nil && slices.Contains(unmaskingFields, field) {
		return refuse("HostConfig.%s may not be set", field)
	}
	if isZero(value) {
		return nil
	}
	check, known := hostConfigFields[field]
	if !known {
		return refuse("HostConfig.%s may not be set", field)
	}
	return check(p, value)
}

// CheckExecCreate refuses an exec that asks for extended privileges.
func CheckExecCreate(body []byte) error {
	var exec struct{ Privileged bool }
	if err := json.Unmarshal(body, &exec); err != nil {
		return refuse("exec create body: %v", err)
	}
	if exec.Privileged {
		return refuse("exec may not be Privileged")
	}
	return nil
}

func (p Policy) networkAllowed(network string) bool {
	if slices.Contains(p.Networks, network) {
		return true
	}
	return p.NetworkPrefix != "" && strings.HasPrefix(network, p.NetworkPrefix) && len(network) > len(p.NetworkPrefix)
}

func checkNetworkMode(p Policy, value any) error {
	mode, _ := value.(string)
	if mode == "default" || mode == "bridge" || mode == "none" || p.networkAllowed(mode) {
		return nil
	}
	return refuse("network mode %q is not one containers may use", mode)
}

// addableCapabilities: an app that drops ALL may still bind ports below 1024.
var addableCapabilities = []string{"NET_BIND_SERVICE", "CAP_NET_BIND_SERVICE"}

func checkCapAdd(_ Policy, value any) error {
	capabilities, _ := value.([]any)
	for _, entry := range capabilities {
		capability, _ := entry.(string)
		if !slices.Contains(addableCapabilities, strings.ToUpper(capability)) {
			return refuse("HostConfig.CapAdd %q is not allowed", capability)
		}
	}
	return nil
}

func checkRuntime(p Policy, value any) error {
	runtime, _ := value.(string)
	if slices.Contains(p.Runtimes, runtime) {
		return nil
	}
	return refuse("HostConfig.Runtime %q is not allowed", runtime)
}

func checkPortBindings(p Policy, value any) error {
	bindings, ok := value.(map[string]any)
	if !ok {
		return refuse("HostConfig.PortBindings is malformed")
	}
	for port, list := range bindings {
		entries, _ := list.([]any)
		for _, entry := range entries {
			binding, _ := entry.(map[string]any)
			hostIP, _ := binding["HostIp"].(string)
			if !slices.Contains(p.PortBindIPs, hostIP) {
				return refuse("port %s may not be published on %q", port, hostIP)
			}
			if hostPort, _ := binding["HostPort"].(string); hostPort != "" {
				return refuse("port %s may not take host port %s; the engine picks one", port, hostPort)
			}
		}
	}
	return nil
}

// checkBinds admits the policy's named volumes only: a source with a path
// separator or a leading dot is a host path.
func checkBinds(p Policy, value any) error {
	binds, _ := value.([]any)
	for _, entry := range binds {
		bind, _ := entry.(string)
		source, _, _ := strings.Cut(bind, ":")
		if source == "" || strings.ContainsAny(source, `/\`) || strings.HasPrefix(source, ".") {
			return refuse("bind %q is a host path; only named volumes may be mounted", bind)
		}
		if err := p.checkVolumeName(source); err != nil {
			return err
		}
	}
	return nil
}

// checkVolumeName admits a named volume only under the policy's prefix.
func (p Policy) checkVolumeName(name string) error {
	if p.VolumePrefix == "" || !strings.HasPrefix(name, p.VolumePrefix) || name == p.VolumePrefix {
		return refuse("volume %q is not one containers may mount", name)
	}
	return nil
}

func checkMounts(p Policy, value any) error {
	mounts, _ := value.([]any)
	for _, entry := range mounts {
		mount, _ := entry.(map[string]any)
		kind, _ := mount["Type"].(string)
		switch kind {
		case "tmpfs":
		case "volume":
			if options, _ := mount["VolumeOptions"].(map[string]any); !isZero(options["DriverConfig"]) {
				return refuse("mount of %v sets a volume driver; only plain named volumes may be mounted", mount["Source"])
			}
			if source, _ := mount["Source"].(string); source != "" {
				if err := p.checkVolumeName(source); err != nil {
					return err
				}
			}
		default:
			return refuse("mount type %q is not allowed; only named volumes and tmpfs", kind)
		}
	}
	return nil
}

// checkSecurityOpt admits options that only tighten the container.
func checkSecurityOpt(_ Policy, value any) error {
	options, _ := value.([]any)
	for _, entry := range options {
		option, _ := entry.(string)
		if option != "no-new-privileges" && option != "no-new-privileges:true" && option != "no-new-privileges=true" {
			return refuse("HostConfig.SecurityOpt %q is not allowed", option)
		}
	}
	return nil
}

func checkLogConfig(_ Policy, value any) error {
	config, _ := value.(map[string]any)
	kind, _ := config["Type"].(string)
	if kind == "" || kind == "json-file" || kind == "local" || kind == "k8s-file" || kind == "journald" {
		return nil
	}
	return refuse("HostConfig.LogConfig type %q is not allowed", kind)
}

func privateOnly(field string, modes ...string) hostConfigCheck {
	return func(_ Policy, value any) error {
		mode, _ := value.(string)
		if slices.Contains(modes, mode) {
			return nil
		}
		return refuse("HostConfig.%s %q is not allowed", field, mode)
	}
}

// isZero reports a JSON value the engine treats as unset.
func isZero(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case bool:
		return !typed
	case float64:
		return typed == 0
	case string:
		return typed == ""
	case []any:
		return len(typed) == 0
	case map[string]any:
		for _, inner := range typed {
			if !isZero(inner) {
				return false
			}
		}
		return true
	}
	return false
}
