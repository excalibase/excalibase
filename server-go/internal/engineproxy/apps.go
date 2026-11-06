package engineproxy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// The calls Containers (app hosting) makes on a single host (EXC-575, ADR
// 0039): per-project networks and disks under their prefixes, the edge joined
// to a project network, and reads of managed containers only.

var (
	networkPath = regexp.MustCompile(`^/networks/` + ref + `(/connect|/disconnect)?$`)
	volumePath  = regexp.MustCompile(`^/volumes/` + ref + `$`)
)

// AppServiceDomain holds the one name each app answers to on its project
// network, <app>.apps.internal; apps search it, so the bare name resolves as a
// Service name does on Kubernetes. The dot keeps it off every container name.
const AppServiceDomain = "apps.internal"

// serviceAlias is one DNS label under AppServiceDomain.
var serviceAlias = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,61}[a-z0-9])?\.` + regexp.QuoteMeta(AppServiceDomain) + `$`)

// checkServiceAlias admits endpoint settings naming exactly one service alias
// that is not one of the edge's platform hosts, and nothing else.
func (p Policy) checkServiceAlias(endpoint any) error {
	settings, ok := endpoint.(map[string]any)
	if !ok {
		return refuse("network endpoint settings must be an object")
	}
	for field, value := range settings {
		if field != "Aliases" && !isZero(value) {
			return refuse("network endpoint %s may not be set", field)
		}
	}
	aliases, _ := settings["Aliases"].([]any)
	if len(aliases) != 1 {
		return refuse("an app may carry exactly one service name")
	}
	name, _ := aliases[0].(string)
	label, _, _ := strings.Cut(name, ".")
	if !serviceAlias.MatchString(name) || strings.HasPrefix(label, p.ContainerPrefix) || p.isEdgeAlias(name) {
		return refuse("service name %q is not one an app may carry", name)
	}
	return nil
}

// isEdgeAlias compares as DNS does, without case.
func (p Policy) isEdgeAlias(name string) bool {
	for _, alias := range p.EdgeAliases {
		if strings.EqualFold(alias, name) {
			return true
		}
	}
	return false
}

// bridgeIsolate is netavark's option keeping a Podman bridge from routing to
// the other bridges; Docker isolates bridges without it.
const bridgeIsolate = "isolate"

// networkCreateFields are the fields a project network may set; any other
// field must be absent or zero, so no IPAM, driver option or scope gets through.
var networkCreateFields = map[string]func(Policy, any) error{
	"Name":           allow, // checked by CheckNetworkCreate
	"Labels":         allow, // checked by CheckNetworkCreate
	"Internal":       allow,
	"CheckDuplicate": allow,
	"Driver": func(_ Policy, value any) error {
		if driver, _ := value.(string); driver != "bridge" {
			return refuse("network Driver %q is not allowed; only bridge", driver)
		}
		return nil
	},
	"Options": func(_ Policy, value any) error {
		options, _ := value.(map[string]any)
		for key, option := range options {
			// checkProjectBridge reads the name.
			allowed := key == bridgeNameOption || (key == bridgeIsolate && option == "true") || (isGatewayMode(key) && option == gatewayIsolated)
			if !allowed {
				return refuse("network Options %s=%v may not be set; only isolate=true or %s=%s", key, option, gatewayModeIPv4, gatewayIsolated)
			}
		}
		return nil
	},
}

// gatewayModeIPv4=isolated (Docker 28+) leaves an internal network's bridge with
// no address on the host, so nothing on it reaches the host's own services (EXC-671);
// the IPv6 option does the same where the daemon enables IPv6 by default (EXC-676).
const (
	gatewayModeIPv4 = "com.docker.network.bridge.gateway_mode_ipv4"
	gatewayModeIPv6 = "com.docker.network.bridge.gateway_mode_ipv6"
	gatewayIsolated = "isolated"
)

func isGatewayMode(key string) bool { return key == gatewayModeIPv4 || key == gatewayModeIPv6 }

// checkIsolatedGateway admits the isolated gateway only on an internal network, where it means "no route anywhere".
func checkIsolatedGateway(create map[string]any) error {
	options, _ := create["Options"].(map[string]any)
	for key := range options {
		if isGatewayMode(key) && create["Internal"] != true {
			return refuse("network Options %s may be set only on an internal network", key)
		}
	}
	return nil
}

// CheckNetworkCreate admits a managed bridge network named under the network prefix.
func (p Policy) CheckNetworkCreate(body []byte) error {
	var create map[string]any
	if err := json.Unmarshal(body, &create); err != nil {
		return refuse("network create body: %v", err)
	}
	if err := checkSpelling("network create", create, networkCreateNames...); err != nil {
		return err
	}
	name, _ := create["Name"].(string)
	if !p.underNetworkPrefix(name) {
		return refuse("network name %q must start with %q", name, p.NetworkPrefix)
	}
	if err := p.requireManagedLabel("network", create["Labels"]); err != nil {
		return err
	}
	if err := checkFields("network create", create, networkCreateFields, p); err != nil {
		return err
	}
	if err := checkIsolatedGateway(create); err != nil {
		return err
	}
	return p.checkProjectBridge(name, create)
}

// checkProjectBridge: a bridge name is only ever the network's own, and under the fence a
// network with a route or a host address has one. Only an internal network with an isolated
// gateway has nothing for the firewall to guard, and the builds' proxy polices itself.
func (p Policy) checkProjectBridge(name string, create map[string]any) error {
	options, _ := create["Options"].(map[string]any)
	bridge, named := options[bridgeNameOption]
	if named {
		if bridge != ProjectBridgeName(name) {
			return refuse("network Options on %q may name the bridge %q alone, not %v", name, ProjectBridgeName(name), bridge)
		}
		return nil
	}
	if !p.FenceProjectBridges || name == p.NetworkPrefix+BuildEgressNetwork {
		return nil
	}
	if create["Internal"] == true && options[gatewayModeIPv4] == gatewayIsolated && options[gatewayModeIPv6] == gatewayIsolated {
		return nil
	}
	return refuse("network %q must name the bridge %q, which the host firewall guards", name, ProjectBridgeName(name))
}

// CheckNetworkConnect admits joining a container with no endpoint settings:
// an alias or a fixed address would answer for something else on the network.
// The one exception is the edge, under the platform's own hostnames (EXC-608).
func (p Policy) CheckNetworkConnect(body []byte) error {
	var member map[string]any
	if err := json.Unmarshal(body, &member); err != nil {
		return refuse("network member body: %v", err)
	}
	if err := checkSpelling("network connect", member, connectNames...); err != nil {
		return err
	}
	endpoint, aliased := member["EndpointConfig"]
	if !aliased || isZero(endpoint) {
		_, err := networkMember(body, false)
		return err
	}
	if container, _ := member["Container"].(string); container == "" || container != p.EdgeContainer {
		return refuse("network EndpointConfig may be set only for the edge")
	}
	if err := checkObjectSpelling("network EndpointConfig", endpoint, endpointNames); err != nil {
		return err
	}
	if err := p.checkEdgeEndpoint(endpoint); err != nil {
		return err
	}
	_, err := networkMember(withoutEndpoint(body), false)
	return err
}

// withoutEndpoint is a connect body with its endpoint settings removed, once CheckNetworkConnect admitted them.
func withoutEndpoint(body []byte) []byte {
	var member map[string]any
	if json.Unmarshal(body, &member) != nil {
		return body
	}
	if _, ok := member["EndpointConfig"]; !ok {
		return body
	}
	delete(member, "EndpointConfig")
	rest, err := json.Marshal(member)
	if err != nil {
		return body
	}
	return rest
}

// checkEdgeEndpoint admits endpoint settings that only name aliases, each one of EdgeAliases.
func (p Policy) checkEdgeEndpoint(endpoint any) error {
	settings, ok := endpoint.(map[string]any)
	if !ok {
		return refuse("network EndpointConfig must be an object")
	}
	for field, value := range settings {
		if field != "Aliases" && !isZero(value) {
			return refuse("network EndpointConfig %s may not be set", field)
		}
	}
	aliases, ok := settings["Aliases"].([]any)
	if !ok || len(aliases) == 0 {
		return refuse("network EndpointConfig may only name aliases")
	}
	for _, alias := range aliases {
		name, _ := alias.(string)
		if !slices.Contains(p.EdgeAliases, name) {
			return refuse("the edge may not answer to %v on a project network", alias)
		}
	}
	return nil
}

// networkMember reads the container a connect or disconnect names; besides it
// only a disconnect's Force may be set.
func networkMember(body []byte, allowForce bool) (string, error) {
	var member map[string]any
	if err := json.Unmarshal(body, &member); err != nil {
		return "", refuse("network member body: %v", err)
	}
	names := connectNames
	if allowForce {
		names = disconnectNames
	}
	if err := checkSpelling("network member", member, names...); err != nil {
		return "", err
	}
	container, _ := member["Container"].(string)
	if container == "" {
		return "", refuse("a network connect or disconnect names a container")
	}
	for field, value := range member {
		switch {
		case field == "Container", isZero(value):
		case field == "Force" && allowForce:
		default:
			return "", refuse("network %s may not be set", field)
		}
	}
	return container, nil
}

var volumeCreateFields = map[string]func(Policy, any) error{
	"Name":   allow, // checked by CheckVolumeCreate
	"Labels": allow, // checked by CheckVolumeCreate
	"Driver": func(_ Policy, value any) error {
		if driver, _ := value.(string); driver != "local" {
			return refuse("volume Driver %q is not allowed; only local", driver)
		}
		return nil
	},
}

// CheckVolumeCreate admits a plain local volume named under the volume prefix.
func (p Policy) CheckVolumeCreate(body []byte) error {
	var create map[string]any
	if err := json.Unmarshal(body, &create); err != nil {
		return refuse("volume create body: %v", err)
	}
	if err := checkSpelling("volume create", create, volumeCreateNames...); err != nil {
		return err
	}
	name, _ := create["Name"].(string)
	if err := p.checkVolumeName(name); err != nil {
		return err
	}
	if err := p.requireManagedLabel("volume", create["Labels"]); err != nil {
		return err
	}
	if err := requireVolumeOwner(create["Labels"]); err != nil {
		return err
	}
	return checkFields("volume create", create, volumeCreateFields, p)
}

func checkFields(what string, body map[string]any, fields map[string]func(Policy, any) error, p Policy) error {
	for field, value := range body {
		check, known := fields[field]
		if !known {
			// SDKs send an empty IPAM object; a struct's zero fields are not a data map's entries.
			if isZero(value) {
				continue
			}
			return refuse("%s field %s may not be set", what, field)
		}
		if !isUnset(value) {
			if err := check(p, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p Policy) requireManagedLabel(what string, raw any) error {
	labels, _ := raw.(map[string]any)
	if labels[p.ManagedLabel] != "true" {
		return refuse("%s must carry the label %s=true", what, p.ManagedLabel)
	}
	return nil
}

func (p Policy) underNetworkPrefix(name string) bool {
	return p.NetworkPrefix != "" && strings.HasPrefix(name, p.NetworkPrefix) && len(name) > len(p.NetworkPrefix)
}

// checkManagedFilter admits a list only when the engine is asked to filter it
// to managed objects, in either form the API accepts for a label filter.
func checkManagedFilter(query url.Values, label string) error {
	raw := query["filters"]
	if len(raw) != 1 {
		return refuse("a list must carry exactly one filter naming %s=true", label)
	}
	// Only to refuse twins: the engine reads the query as sent.
	if _, err := canonicalBody([]byte(raw[0])); err != nil {
		return refuse("list filters: %v", err)
	}
	var filters map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw[0]), &filters); err != nil {
		return refuse("list filters: %v", err)
	}
	want := label + "=true"
	var asList []string
	if json.Unmarshal(filters["label"], &asList) == nil && slices.Contains(asList, want) {
		return nil
	}
	var asSet map[string]bool
	if json.Unmarshal(filters["label"], &asSet) == nil && asSet[want] {
		return nil
	}
	return refuse("a list must be filtered to %s", want)
}

// authorizeApps decides the app-hosting calls; handled is false for any other path.
func (h *Handler) authorizeApps(r *http.Request, api string) (handled bool, err error) {
	switch {
	case api == "/info":
		return true, methodOnly(r, http.MethodGet)
	case api == "/containers/json" && r.Method == http.MethodGet:
		return true, checkManagedFilter(r.URL.Query(), h.policy.ManagedLabel)
	case api == "/networks/create":
		if err := methodOnly(r, http.MethodPost); err != nil {
			return true, err
		}
		return true, h.checkBody(r, h.policy.CheckNetworkCreate)
	case api == "/networks":
		if err := methodOnly(r, http.MethodGet); err != nil {
			return true, err
		}
		return true, checkManagedFilter(r.URL.Query(), h.policy.ManagedLabel)
	case api == "/volumes/create":
		if err := methodOnly(r, http.MethodPost); err != nil {
			return true, err
		}
		return true, h.checkBody(r, h.policy.CheckVolumeCreate)
	case api == "/volumes":
		if err := methodOnly(r, http.MethodGet); err != nil {
			return true, err
		}
		return true, checkManagedFilter(r.URL.Query(), h.policy.ManagedLabel)
	}
	if match := networkPath.FindStringSubmatch(api); match != nil {
		return true, h.authorizeNetwork(r, match[1], match[2])
	}
	if match := volumePath.FindStringSubmatch(api); match != nil {
		if r.Method != http.MethodGet && r.Method != http.MethodDelete {
			return true, refuse("%s /volumes/{name} is not a call provisioning makes", r.Method)
		}
		return true, h.policy.checkVolumeName(match[1])
	}
	return false, nil
}

func (h *Handler) authorizeNetwork(r *http.Request, name, action string) error {
	if !h.policy.underNetworkPrefix(name) {
		return refuse("network %q is not a project network", name)
	}
	switch {
	case action == "" && (r.Method == http.MethodGet || r.Method == http.MethodDelete):
		return nil
	case action == "/connect" && r.Method == http.MethodPost:
		return h.checkBody(r, func(body []byte) error { return h.checkMember(r, name, body, false) })
	case action == "/disconnect" && r.Method == http.MethodPost:
		return h.checkBody(r, func(body []byte) error { return h.checkMember(r, name, body, true) })
	}
	return refuse("%s /networks/{name}%s is not a call provisioning makes", r.Method, action)
}

// checkMember admits the named edge or a managed container, and nothing else.
func (h *Handler) checkMember(r *http.Request, network string, body []byte, disconnect bool) error {
	if !disconnect {
		if rejoin, err := h.checkAppRejoin(r, network, body); rejoin || err != nil {
			return err
		}
		if err := h.policy.CheckNetworkConnect(body); err != nil {
			return err
		}
		body = withoutEndpoint(body)
	}
	container, err := networkMember(body, disconnect)
	if err != nil {
		return err
	}
	if h.policy.EdgeContainer != "" && container == h.policy.EdgeContainer {
		return nil
	}
	if !disconnect && h.policy.FenceProjectBridges && network == h.policy.NetworkPrefix+BuildEgressNetwork {
		found, err := h.inspectGuarded(r.Context(), container)
		if err != nil {
			return err
		}
		if len(found.Config.Entrypoint) == 0 || found.Config.Entrypoint[0] != BuildEgressEntrypoint {
			return refuse("network %q is the builds' proxy's alone", network)
		}
	}
	return h.requireManaged(r.Context(), container)
}

// appNameLabel names an app on a container: its service name is that, under AppServiceDomain.
const appNameLabel = "excalibase.app-name"

// checkAppRejoin admits a managed app back on the network it runs on, under its own service
// name and no other, as a create admits it; a remade network takes every member off.
func (h *Handler) checkAppRejoin(r *http.Request, network string, body []byte) (bool, error) {
	var member map[string]any
	if err := json.Unmarshal(body, &member); err != nil {
		return false, refuse("network member body: %v", err)
	}
	endpoint, aliased := member["EndpointConfig"]
	name, _ := member["Container"].(string)
	if !aliased || isZero(endpoint) || name == "" || name == h.policy.EdgeContainer || h.policy.checkServiceAlias(endpoint) != nil {
		return false, nil
	}
	if err := checkSpelling("network connect", member, connectNames...); err != nil {
		return true, err
	}
	if err := checkObjectSpelling("network EndpointConfig", endpoint, endpointNames); err != nil {
		return true, err
	}
	found, err := h.inspectGuarded(r.Context(), name)
	if err != nil {
		return true, err
	}
	aliases, _ := endpoint.(map[string]any)["Aliases"].([]any)
	want := found.Config.Labels[appNameLabel] + "." + AppServiceDomain
	if found.Config.Labels[h.policy.ManagedLabel] != "true" || found.Host.NetworkMode != network || aliases[0] != want {
		return true, refuse("%s may rejoin only its own network under its own service name", name)
	}
	return true, nil
}

func methodOnly(r *http.Request, method string) error {
	if r.Method != method {
		return refuse("%s %s is not a call provisioning makes", r.Method, r.URL.Path)
	}
	return nil
}
