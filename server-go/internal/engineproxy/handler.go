package engineproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"time"
)

// maxCreateBody bounds the JSON the proxy reads to check a create.
const maxCreateBody = 1 << 20

var (
	versionPrefix = regexp.MustCompile(`^/v[0-9]+\.[0-9]+(/.*)$`)
	// A container or exec reference: an id or a name, never "." or "..".
	ref            = `([A-Za-z0-9][A-Za-z0-9_.-]*)`
	containerPath  = regexp.MustCompile(`^/containers/` + ref + `(/[a-z]+)?$`)
	execPath       = regexp.MustCompile(`^/exec/` + ref + `/(start|json)$`)
	errNotManaged  = errors.New("not a container provisioning manages")
	errNoSuchThing = errors.New("no such container")
)

// route is what the proxy does with one method on a container sub-path
// ("" is the container itself).
type route int

const (
	routeManaged route = iota
	routeExecCreate
)

// containerRoutes are the calls provisioning makes on a container it created.
var containerRoutes = map[string]route{
	"DELETE ":       routeManaged,
	"GET /json":     routeManaged,
	"POST /start":   routeManaged,
	"POST /stop":    routeManaged,
	"PUT /archive":  routeManaged,
	"GET /archive":  routeManaged,
	"HEAD /archive": routeManaged,
	"POST /exec":    routeExecCreate,
	"GET /logs":     routeManaged,
	"POST /wait":    routeManaged,
}

// Handler is the proxy. Build it with NewHandler.
type Handler struct {
	policy    Policy
	target    *url.URL
	transport http.RoundTripper
	forward   *httputil.ReverseProxy
	dial      func(ctx context.Context) (net.Conn, error)
}

// NewHandler proxies allowed calls to target through transport.
func NewHandler(policy Policy, target *url.URL, transport http.RoundTripper) *Handler {
	forward := &httputil.ReverseProxy{
		Rewrite: func(out *httputil.ProxyRequest) {
			out.SetURL(target)
			out.Out.URL.Path = out.In.URL.Path
			out.Out.URL.RawPath = out.In.URL.RawPath
			out.Out.Host = target.Host
		},
		Transport: transport,
		// Image pulls and exec output stream; flush as bytes arrive.
		FlushInterval: -1,
	}
	return &Handler{policy: policy, target: target, transport: transport, forward: forward}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := h.authorize(r); err != nil {
		h.refuse(w, r, err)
		return
	}
	if h.dial != nil && isExecUpgrade(r) {
		h.splice(w, r)
		return
	}
	h.forward.ServeHTTP(w, r)
}

func (h *Handler) authorize(r *http.Request) error {
	raw := r.URL.Path
	if path.Clean(raw) != raw || r.URL.RawPath != "" {
		return refuse("path %q is not canonical", r.URL.EscapedPath())
	}
	api := stripVersion(raw)
	switch {
	case api == "/_ping" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		return nil
	case api == "/version" && r.Method == http.MethodGet:
		return nil
	case api == "/images/json" && r.Method == http.MethodGet:
		return nil
	case api == "/images/create" && r.Method == http.MethodPost:
		return nil
	case api == "/containers/create" && r.Method == http.MethodPost:
		names := r.URL.Query()["name"]
		if len(names) != 1 {
			return refuse("a container create names exactly one container")
		}
		return h.checkBody(r, func(body []byte) error {
			return h.policy.CheckCreate(names[0], body)
		})
	}
	if handled, err := h.authorizeApps(r, api); handled {
		return err
	}
	if match := execPath.FindStringSubmatch(api); match != nil {
		return h.authorizeExec(r, match[1], match[2])
	}
	if match := containerPath.FindStringSubmatch(api); match != nil {
		return h.authorizeContainer(r, match[1], match[2])
	}
	return refuse("%s %s is not a call provisioning makes", r.Method, api)
}

func (h *Handler) authorizeContainer(r *http.Request, id, action string) error {
	if id == "create" || id == "json" {
		return refuse("%s /containers/%s is not a call provisioning makes", r.Method, id)
	}
	kind, ok := containerRoutes[r.Method+" "+action]
	if !ok {
		return refuse("%s /containers/{id}%s is not a call provisioning makes", r.Method, action)
	}
	if err := h.requireManaged(r.Context(), id); err != nil {
		return err
	}
	if kind == routeExecCreate {
		return h.checkBody(r, CheckExecCreate)
	}
	return nil
}

func (h *Handler) authorizeExec(r *http.Request, id, action string) error {
	allowed := (action == "start" && r.Method == http.MethodPost) ||
		(action == "json" && r.Method == http.MethodGet)
	if !allowed {
		return refuse("%s /exec/{id}/%s is not a call provisioning makes", r.Method, action)
	}
	var exec struct{ ContainerID string }
	if err := h.inspect(r.Context(), "/exec/"+id+"/json", &exec); err != nil {
		return err
	}
	return h.requireManaged(r.Context(), exec.ContainerID)
}

func (h *Handler) requireManaged(ctx context.Context, id string) error {
	var container struct {
		Config struct{ Labels map[string]string }
	}
	if err := h.inspect(ctx, "/containers/"+url.PathEscape(id)+"/json", &container); err != nil {
		return err
	}
	if container.Config.Labels[h.policy.ManagedLabel] != "true" {
		return fmt.Errorf("%w: %s", errNotManaged, id)
	}
	return nil
}

// inspect reads an engine object the proxy needs to decide on a call.
func (h *Handler) inspect(ctx context.Context, apiPath string, into any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	target := *h.target
	target.Path = apiPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	resp, err := h.transport.RoundTrip(req)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", apiPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNoSuchThing
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("inspect %s: engine answered %d", apiPath, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxCreateBody)).Decode(into)
}

func (h *Handler) checkBody(r *http.Request, check func([]byte) error) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxCreateBody+1))
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	if len(body) > maxCreateBody {
		return refuse("request body is larger than %d bytes", maxCreateBody)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return check(body)
}

func (h *Handler) refuse(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusForbidden
	switch {
	case errors.Is(err, errNoSuchThing):
		status = http.StatusNotFound
	case !errors.Is(err, ErrRefused) && !errors.Is(err, errNotManaged):
		status = http.StatusBadGateway
	}
	log.Printf("engine proxy: %d %s %s: %v", status, r.Method, r.URL.Path, err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"message": err.Error()})
}

// stripVersion drops the /v1.NN prefix the API accepts on every path.
func stripVersion(raw string) string {
	if match := versionPrefix.FindStringSubmatch(raw); match != nil {
		return match[1]
	}
	return raw
}
