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
			allowed := (key == bridgeIsolate && option == "true") || (key == gatewayModeIPv4 && option == gatewayIsolated)
			if !allowed {
				return refuse("network Options %s=%v may not be set; only isolate=true or %s=%s", key, option, gatewayModeIPv4, gatewayIsolated)
			}
		}
		return nil
	},
}

// gatewayModeIPv4=isolated (Docker 28+) leaves an internal network's bridge with
// no address on the host, so nothing on it reaches the host's own services (EXC-671).
const (
	gatewayModeIPv4 = "com.docker.network.bridge.gateway_mode_ipv4"
	gatewayIsolated = "isolated"
)

// checkIsolatedGateway admits the isolated gateway only on an internal network, where it means "no route anywhere".
func checkIsolatedGateway(create map[string]any) error {
	options, _ := create["Options"].(map[string]any)
	if _, set := options[gatewayModeIPv4]; set && create["Internal"] != true {
		return refuse("network Options %s may be set only on an internal network", gatewayModeIPv4)
	}
	return nil
}

// CheckNetworkCreate admits a managed bridge network named under the network prefix.
func (p Policy) CheckNetworkCreate(body []byte) error {
	var create map[string]any
	if err := json.Unmarshal(body, &create); err != nil {
		return refuse("network create body: %v", err)
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
	return checkIsolatedGateway(create)
}

// CheckNetworkConnect admits joining a container with no endpoint settings:
// an alias or a fixed address would answer for something else on the network.
// The one exception is the edge, under the platform's own hostnames (EXC-608).
func (p Policy) CheckNetworkConnect(body []byte) error {
	var member map[string]any
	if err := json.Unmarshal(body, &member); err != nil {
		return refuse("network member body: %v", err)
	}
	endpoint, aliased := member["EndpointConfig"]
	if !aliased || isZero(endpoint) {
		_, err := networkMember(body, false)
		return err
	}
	if container, _ := member["Container"].(string); container == "" || container != p.EdgeContainer {
		return refuse("network EndpointConfig may be set only for the edge")
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
	name, _ := create["Name"].(string)
	if err := p.checkVolumeName(name); err != nil {
		return err
	}
	if err := p.requireManagedLabel("volume", create["Labels"]); err != nil {
		return err
	}
	return checkFields("volume create", create, volumeCreateFields, p)
}

func checkFields(what string, body map[string]any, fields map[string]func(Policy, any) error, p Policy) error {
	for field, value := range body {
		check, known := fields[field]
		if !known {
			if isZero(value) {
				continue
			}
			return refuse("%s field %s may not be set", what, field)
		}
		if !isZero(value) {
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
		return h.checkBody(r, func(body []byte) error { return h.checkMember(r, body, false) })
	case action == "/disconnect" && r.Method == http.MethodPost:
		return h.checkBody(r, func(body []byte) error { return h.checkMember(r, body, true) })
	}
	return refuse("%s /networks/{name}%s is not a call provisioning makes", r.Method, action)
}

// checkMember admits the named edge or a managed container, and nothing else.
func (h *Handler) checkMember(r *http.Request, body []byte, disconnect bool) error {
	if !disconnect {
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
	return h.requireManaged(r.Context(), container)
}

func methodOnly(r *http.Request, method string) error {
	if r.Method != method {
		return refuse("%s %s is not a call provisioning makes", r.Method, r.URL.Path)
	}
	return nil
}
