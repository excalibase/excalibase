package mcpserver

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
	"regexp"
	"strings"
	"time"
)

const (
	maxProbeBody        = 8 << 10
	maxProbeRequestBody = 16 << 10
	maxProbeQuery       = 4 << 10
	maxProbeHeader      = 256
	maxProbeToken       = 8 << 10
	probeTimeout        = 15 * time.Second
	publishablePrefix   = "esk_pub_"
)

var (
	// probePath is a table, or rpc/<function> for a tracked database function.
	probePath = regexp.MustCompile(`^(rpc/)?[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	// probeJWT is the shape of an end user's access token; anything else (a
	// personal access token, a secret key) is never forwarded.
	probeJWT = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
	// graphQLMutation is read broadly: a read-only probe refuses any document
	// that names a mutation, even inside a string.
	graphQLMutation    = regexp.MustCompile(`(?i)\bmutation\b`)
	graphQLRequestKeys = map[string]bool{"query": true, "variables": true, "operationName": true}
	errProbeUnanswered = errors.New("the project's data API did not answer")
	noRedirects        = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
)

type probeArgs struct {
	projectArg
	PublishableKey string `json:"publishable_key,omitempty" jsonschema:"the publishable key the page uses (esk_pub_...); the probe signs in with it as the anon role, as the page does"`
	AccessToken    string `json:"access_token,omitempty" jsonschema:"an end user's access token (the accessToken a test sign-in answered) to send the request as that signed-in user instead of anon"`
	API            string `json:"api,omitempty" jsonschema:"rest (default) or graphql"`
	Method         string `json:"method,omitempty" jsonschema:"REST method: GET (default) or HEAD; POST, PATCH and DELETE on a read-write connection"`
	Path           string `json:"path,omitempty" jsonschema:"REST: the table, e.g. todos, or rpc/<function> for a tracked database function"`
	Query          string `json:"query,omitempty" jsonschema:"REST query string, e.g. select=id,title&order=created_at.desc&limit=20&completed=eq.false"`
	Prefer         string `json:"prefer,omitempty" jsonschema:"the Prefer header the page sends, e.g. count=exact or return=representation"`
	Origin         string `json:"origin,omitempty" jsonschema:"the page's origin, e.g. its app URL or http://localhost:5173: the probe then also runs the browser's CORS preflights for the sign-in call and this request"`
	Body           string `json:"body,omitempty" jsonschema:"JSON body: the row(s) for a REST write, or {\"query\": ..., \"variables\": ...} for GraphQL"`
}

const probeDescription = "Send ONE request to the project's own data API (REST, rpc/<function> or GraphQL) the way a page does: " +
	"sign in with a publishable key as the anon role, or pass a signed-in end user's access_token, then call the endpoint, and see the status and body. " +
	"Permissions apply exactly as for the page; with origin, the browser's CORS preflights for the sign-in call and the request are checked too. " +
	"Probe every request a page makes before handing it over; " +
	"a 403 usually means a missing permission (set_permission), and a count needs allowAggregations."

func probeTools() []entry {
	return []entry{
		tool("test_api_request", probeDescription, writeTool, testAPIRequest),
		tool("test_api_request", probeDescription+" On this read-only connection it only reads.", readOnlyVariant, testAPIRequest),
	}
}

type probeRequest struct {
	method, path, query string
	body                []byte
	origin              string
}

// testAPIRequest runs one request against the project's own data plane. The
// target is built from the configured data-plane base and the project id
// alone; the caller picks only the table, query and headers, and a redirect
// is never followed, so nothing but this project's API is reached.
func testAPIRequest(ctx context.Context, c *call, in probeArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	base, err := dataPlaneBase(c.settings)
	if err != nil {
		return nil, err
	}
	target, err := probeTarget(c.caller.ReadOnly, projectID, in)
	if err != nil {
		return nil, err
	}
	if !c.probes.allow(c.caller.User.ID) {
		return nil, fmt.Errorf("too many probes: at most %d at once, then one every %s", probeBurst, probeEvery)
	}
	var info struct {
		OrgSlug string `json:"orgSlug"`
	}
	if err := c.get(ctx, projectsAPI+projectID+"/info/", nil, &info); err != nil {
		return nil, err
	}
	if info.OrgSlug == "" {
		return nil, errors.New("the project has no organization to sign in through")
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	client := &http.Client{CheckRedirect: noRedirects}
	tokenURL := endpoints(base, info.OrgSlug, projectID)["auth"] + "/token"
	token := in.AccessToken
	if token == "" {
		if token, err = exchangeKey(ctx, client, tokenURL, in.PublishableKey); err != nil {
			return nil, err
		}
	}
	out, err := sendProbe(ctx, client, base, target, in, token)
	if err != nil || target.origin == "" {
		return out, err
	}
	out["cors"] = corsPreflights(ctx, client, tokenURL, base, target, in)
	return out, nil
}

// dataPlaneBase is the configured data plane; with none configured the probe
// is off rather than sent to a guessed address.
func dataPlaneBase(settings Settings) (string, error) {
	base, err := url.Parse(settings.DataPlaneURL)
	if settings.DataPlaneURL == "" || err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return "", errors.New("test_api_request is not available: this server has no data plane address configured")
	}
	return strings.TrimRight(settings.DataPlaneURL, "/"), nil
}

func probeTarget(readOnly bool, projectID string, in probeArgs) (probeRequest, error) {
	if err := probeCredential(projectID, in); err != nil {
		return probeRequest{}, err
	}
	if len(in.Prefer) > maxProbeHeader || strings.ContainsAny(in.Prefer+in.PublishableKey, "\r\n") {
		return probeRequest{}, fmt.Errorf("prefer must be one line of at most %d characters", maxProbeHeader)
	}
	origin, err := probeOrigin(in.Origin)
	if err != nil {
		return probeRequest{}, err
	}
	if in.Body != "" && (len(in.Body) > maxProbeRequestBody || !json.Valid([]byte(in.Body))) {
		return probeRequest{}, fmt.Errorf("body must be JSON of at most %d bytes", maxProbeRequestBody)
	}
	var target probeRequest
	switch in.API {
	case "graphql":
		target, err = graphQLTarget(readOnly, projectID, in.Body)
	case "", "rest":
		target, err = restTarget(readOnly, projectID, in)
	default:
		err = errors.New("api must be rest or graphql")
	}
	target.origin = origin
	return target, err
}

// probeCredential is a publishable key, an end user's access token, or both.
func probeCredential(projectID string, in probeArgs) error {
	if in.PublishableKey == "" && in.AccessToken == "" {
		return errors.New("pass publishable_key (anon) or access_token (a signed-in end user)")
	}
	if in.PublishableKey != "" && (!strings.HasPrefix(in.PublishableKey, publishablePrefix) || len(in.PublishableKey) > maxProbeHeader) {
		return fmt.Errorf("publishable_key must be a publishable key (%s...); secret keys are never used", publishablePrefix)
	}
	if in.AccessToken != "" {
		return endUserToken(in.AccessToken, projectID)
	}
	return nil
}

// probeOrigin accepts an origin only and sends it as scheme://host.
func probeOrigin(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	origin, err := url.Parse(raw)
	if err != nil || len(raw) > maxProbeHeader || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" ||
		origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.Fragment != "" {
		return "", errors.New("origin must be an origin, e.g. https://todo.example.com")
	}
	return origin.Scheme + "://" + origin.Host, nil
}

// graphQLTarget sends a re-encoded document, so the engine reads exactly what
// was checked. Keys must be named exactly: Go matches field names without
// case, and the engine would read a different "query".
func graphQLTarget(readOnly bool, projectID, body string) (probeRequest, error) {
	refused := errors.New(`body must be one {"query": ..., "variables": ...} object`)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		return probeRequest{}, refused
	}
	var document struct {
		Query         string          `json:"query"`
		Variables     json.RawMessage `json:"variables,omitempty"`
		OperationName string          `json:"operationName,omitempty"`
	}
	for key := range fields {
		if !graphQLRequestKeys[key] {
			return probeRequest{}, refused
		}
	}
	if err := json.Unmarshal([]byte(body), &document); err != nil || document.Query == "" {
		return probeRequest{}, refused
	}
	if readOnly && graphQLMutation.MatchString(document.Query) {
		return probeRequest{}, errors.New("this connection is read-only: a GraphQL mutation is refused")
	}
	canonical, err := json.Marshal(document)
	if err != nil {
		return probeRequest{}, refused
	}
	return probeRequest{method: http.MethodPost, path: "/" + projectID + "/graphql", body: canonical}, nil
}

func restTarget(readOnly bool, projectID string, in probeArgs) (probeRequest, error) {
	method := strings.ToUpper(in.Method)
	switch method {
	case "":
		method = http.MethodGet
	case http.MethodGet, http.MethodHead:
	case http.MethodPost, http.MethodPatch, http.MethodDelete:
		if readOnly {
			return probeRequest{}, errors.New("this connection is read-only: only GET and HEAD are sent")
		}
	default:
		return probeRequest{}, errors.New("method must be GET, HEAD, POST, PATCH or DELETE")
	}
	if !probePath.MatchString(in.Path) {
		return probeRequest{}, errors.New("path must be a table name, e.g. todos, or rpc/<function>")
	}
	query, err := url.ParseQuery(in.Query)
	if err != nil || len(in.Query) > maxProbeQuery {
		return probeRequest{}, fmt.Errorf("query must be a URL query string of at most %d characters, e.g. select=id&order=id.desc", maxProbeQuery)
	}
	target := probeRequest{method: method, path: "/" + projectID + "/api/v1/" + in.Path, query: query.Encode()}
	if in.Body != "" {
		if method == http.MethodGet || method == http.MethodHead {
			return probeRequest{}, errors.New("a GET or HEAD request has no body")
		}
		target.body = []byte(in.Body)
	}
	return target, nil
}

// exchangeKey signs in with the publishable key, as the SDK does.
func exchangeKey(ctx context.Context, client *http.Client, tokenURL, key string) (string, error) {
	body, err := json.Marshal(map[string]string{"grant_type": "api_key", "api_key": key})
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		log.Printf("mcp: probe token request: %v", err)
		return "", errProbeUnanswered
	}
	request.Header.Set("Content-Type", "application/json")
	answer, err := client.Do(request)
	if err != nil {
		log.Printf("mcp: probe token exchange: %v", err)
		return "", errors.New("the project's auth API did not answer")
	}
	defer answer.Body.Close()
	var session struct {
		AccessToken string `json:"accessToken"`
	}
	if answer.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(answer.Body, maxProbeBody)).Decode(&session) != nil || session.AccessToken == "" {
		return "", fmt.Errorf("the publishable key was refused by the project's auth API (HTTP %d)", answer.StatusCode)
	}
	return session.AccessToken, nil
}

func sendProbe(ctx context.Context, client *http.Client, base string, target probeRequest, in probeArgs, token string) (map[string]any, error) {
	path := target.path
	if target.query != "" {
		path += "?" + target.query
	}
	var body io.Reader
	if target.body != nil {
		body = bytes.NewReader(target.body)
	}
	request, err := http.NewRequestWithContext(ctx, target.method, base+path, body)
	if err != nil {
		log.Printf("mcp: probe request %s: %v", target.path, err)
		return nil, errProbeUnanswered
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if in.PublishableKey != "" {
		request.Header.Set("X-Excalibase-Publishable-Key", in.PublishableKey)
	}
	request.Header.Set("Accept", "application/json")
	if target.body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if in.Prefer != "" {
		request.Header.Set("Prefer", in.Prefer)
	}
	if target.origin != "" {
		request.Header.Set("Origin", target.origin)
	}
	answer, err := client.Do(request)
	if err != nil {
		log.Printf("mcp: probe %s %s: %v", target.method, target.path, err)
		return nil, errProbeUnanswered
	}
	defer answer.Body.Close()
	read, err := io.ReadAll(io.LimitReader(answer.Body, maxProbeBody+1))
	if err != nil {
		return nil, errors.New("the project's data API answer could not be read")
	}
	truncated := len(read) > maxProbeBody
	if truncated {
		read = read[:maxProbeBody]
	}
	return map[string]any{
		"request":   target.method + " " + path,
		"status":    answer.StatusCode,
		"headers":   probeHeaders(answer.Header),
		"body":      string(read),
		"truncated": truncated,
	}, nil
}

// probeHeaders keeps the answer headers that explain a page's behaviour.
func probeHeaders(header http.Header) map[string]string {
	out := map[string]string{}
	for _, name := range []string{"Content-Type", "Access-Control-Allow-Origin", "Content-Range", "Preference-Applied"} {
		if value := header.Get(name); value != "" {
			out[name] = value
		}
	}
	return out
}
