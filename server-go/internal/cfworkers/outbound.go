package cfworkers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

type scriptSettings struct {
	Bindings []struct {
		Type      string `json:"type"`
		Namespace string `json:"namespace"`
		Outbound  *struct {
			Worker struct {
				Service string `json:"service"`
			} `json:"worker"`
		} `json:"outbound"`
	} `json:"bindings"`
}

// DispatchOutbound answers the outbound Worker the router script's binding of
// this client's namespace names, "" when it names none. The shape follows the
// upload metadata Cloudflare documents; it is unverified on a live account (EXC-661 R5).
func (c *Client) DispatchOutbound(ctx context.Context, routerScript string) (string, error) {
	if !scriptNamePattern.MatchString(routerScript) {
		return "", fmt.Errorf("invalid worker script name %q", routerScript)
	}
	_, raw, err := c.call(ctx, http.MethodGet, c.accountPath("workers", "scripts", routerScript, "settings"), c.token, "", nil)
	if err != nil {
		return "", err
	}
	var settings scriptSettings
	if err := json.Unmarshal(raw, &settings); err != nil {
		return "", fmt.Errorf("decode the router's settings: %w", err)
	}
	for _, binding := range settings.Bindings {
		if binding.Type == "dispatch_namespace" && binding.Namespace == c.namespace && binding.Outbound != nil {
			return binding.Outbound.Worker.Service, nil
		}
	}
	return "", nil
}
