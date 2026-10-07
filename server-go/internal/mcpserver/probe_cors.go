package mcpserver

import (
	"context"
	"io"
	"log"
	"net/http"
	"strings"
)

// preflight is what a browser learns from one CORS preflight.
type preflight struct {
	Request     string `json:"request"`
	Status      int    `json:"status,omitempty"`
	AllowOrigin string `json:"allowOrigin,omitempty"`
	Allowed     bool   `json:"allowed"`
	Error       string `json:"error,omitempty"`
}

// corsPreflights runs the preflights a browser on target.origin sends before
// signing in and before this request, since both carry non-simple headers.
func corsPreflights(ctx context.Context, client *http.Client, tokenURL, base string, target probeRequest, in probeArgs, withCredential bool) map[string]any {
	var requestHeaders []string
	if withCredential {
		requestHeaders = append(requestHeaders, "authorization")
	}
	if in.PublishableKey != "" {
		requestHeaders = append(requestHeaders, "x-excalibase-publishable-key")
	}
	if target.body != nil {
		requestHeaders = append(requestHeaders, "content-type")
	}
	if in.Prefer != "" {
		requestHeaders = append(requestHeaders, "prefer")
	}
	out := map[string]any{}
	allowed := true
	// An anonymous request with no credential never signs in, so there is no sign-in call to check.
	if withCredential {
		signIn := sendPreflight(ctx, client, tokenURL, target.origin, http.MethodPost, "content-type")
		out["signIn"] = signIn
		allowed = signIn.Allowed
	}
	request := sendPreflight(ctx, client, base+target.path, target.origin, target.method, strings.Join(requestHeaders, ","))
	out["request"] = request
	allowed = allowed && request.Allowed
	if !allowed {
		out["blocked"] = blockedAdvice(target.origin, false)
	}
	return out
}

const corsPropagation = "CORS changes take about 1 minute to reach the data plane."

// blockedAdvice is what to do about a browser origin the data plane refused.
func blockedAdvice(origin string, listed bool) string {
	if listed {
		return "A browser page on " + origin + " would be blocked here, but the origin is already listed on the project's CORS allowlist: " +
			"wait about 1 minute for the change to reach the data plane, then probe again. " + corsPropagation
	}
	return "A browser page on " + origin + " would be blocked here. Add the origin with add_cors_origin " +
		"(the allowlist covers sign-in and data alike), then probe again. " + corsPropagation
}

// adviseOnBlock rewrites a block's advice when the origin is already on the
// allowlist: adding it again would change nothing, only waiting does.
func adviseOnBlock(ctx context.Context, c *call, projectID, origin string, cors map[string]any) {
	if cors["blocked"] == nil {
		return
	}
	var current corsList
	if err := c.get(ctx, projectsAPI+projectID+"/cors/", nil, &current); err != nil {
		return
	}
	for _, listed := range current.AllowedOrigins {
		if strings.EqualFold(strings.TrimRight(listed, "/"), origin) {
			cors["blocked"] = blockedAdvice(origin, true)
			return
		}
	}
}

func sendPreflight(ctx context.Context, client *http.Client, targetURL, origin, method, headers string) preflight {
	request, err := http.NewRequestWithContext(ctx, http.MethodOptions, targetURL, nil)
	if err != nil {
		log.Printf("mcp: probe preflight request: %v", err)
		return preflight{Error: errProbeUnanswered.Error()}
	}
	result := preflight{Request: "OPTIONS " + request.URL.Path}
	request.Header.Set("Origin", origin)
	request.Header.Set("Access-Control-Request-Method", method)
	request.Header.Set("Access-Control-Request-Headers", headers)
	answer, err := client.Do(request)
	if err != nil {
		log.Printf("mcp: probe preflight: %v", err)
		result.Error = errProbeUnanswered.Error()
		return result
	}
	defer answer.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(answer.Body, maxProbeBody))
	result.Status = answer.StatusCode
	result.AllowOrigin = answer.Header.Get("Access-Control-Allow-Origin")
	originAllowed := result.AllowOrigin == origin || result.AllowOrigin == "*"
	methodAllowed := simpleMethod(method) || allowsAll(answer.Header.Get("Access-Control-Allow-Methods"), method)
	headersAllowed := allowsAll(answer.Header.Get("Access-Control-Allow-Headers"), strings.Split(headers, ",")...)
	result.Allowed = answer.StatusCode < 300 && originAllowed && methodAllowed && headersAllowed
	if answer.StatusCode < 300 && originAllowed && !result.Allowed {
		result.Error = "the origin is allowed, but not this method or these headers: " + method + " " + headers
	}
	return result
}

func simpleMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodPost
}

// allowsAll reports whether a preflight's allow list (comma separated, any
// case) names every wanted value. * covers everything but Authorization,
// which a browser needs listed by name.
func allowsAll(allowList string, wanted ...string) bool {
	allowed := map[string]bool{}
	for _, value := range strings.Split(allowList, ",") {
		allowed[strings.ToLower(strings.TrimSpace(value))] = true
	}
	for _, value := range wanted {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || allowed[value] || (allowed["*"] && value != "authorization") {
			continue
		}
		return false
	}
	return true
}
