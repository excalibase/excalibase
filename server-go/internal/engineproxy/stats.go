package engineproxy

import (
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
)

// Host monitoring (EXC-670) reads one-shot stats of every managed container
// and of the platform's own containers named in Policy.StatsContainers. A
// platform container gets stats and a state-only inspect, never its env,
// mounts, logs or exec.

var platformInspectPath = regexp.MustCompile(`^/containers/` + ref + `/json$`)

// statsQuery is the one shape a stats call may take: a single sample.
func statsQuery(query url.Values) error {
	for name, values := range query {
		if len(values) != 1 {
			return refuse("stats %s is given more than once", name)
		}
		switch name {
		case "stream":
			if values[0] != "false" && values[0] != "0" {
				return refuse("stats must be one sample: stream=false")
			}
		case "one-shot":
			if values[0] != "true" && values[0] != "1" && values[0] != "false" && values[0] != "0" {
				return refuse("stats one-shot must be true or false")
			}
		default:
			return refuse("stats takes stream and one-shot only")
		}
	}
	if _, ok := query["stream"]; !ok {
		return refuse("stats must be one sample: stream=false")
	}
	return nil
}

// monitoredPlatform is a platform container host monitoring reads, named exactly.
func (p Policy) monitoredPlatform(name string) bool {
	return slices.Contains(p.StatsContainers, name)
}

func (h *Handler) authorizeStats(r *http.Request, id string) error {
	if err := statsQuery(r.URL.Query()); err != nil {
		return err
	}
	if h.policy.monitoredPlatform(id) {
		return nil
	}
	return h.requireManaged(r.Context(), id)
}

// platformState is all of a platform container's inspect that leaves the proxy.
type platformState struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	State struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
		Health  *struct {
			Status string `json:"Status"`
		} `json:"Health,omitempty"`
	} `json:"State"`
	Config struct {
		Image string `json:"Image"`
	} `json:"Config"`
}

// servePlatformInspect answers GET /containers/{name}/json for a monitored
// platform container with its state alone; it reports whether it answered.
func (h *Handler) servePlatformInspect(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || r.URL.RawPath != "" {
		return false
	}
	match := platformInspectPath.FindStringSubmatch(stripVersion(r.URL.Path))
	if match == nil || !h.policy.monitoredPlatform(match[1]) {
		return false
	}
	if len(r.URL.Query()) > 0 {
		h.refuse(w, r, refuse("a platform container's inspect takes no parameters"))
		return true
	}
	var state platformState
	if err := h.inspect(r.Context(), "/containers/"+url.PathEscape(match[1])+"/json", &state); err != nil {
		h.refuse(w, r, err)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(state)
	return true
}
