package mcpserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

const (
	statusRollingOut = "ROLLING_OUT"
	stateRollingOut  = "rolling out"
	stateNotAnswered = "running, URL not answering yet"
	urlProbeTimeout  = 3 * time.Second
)

const ciliumMissingMessage = "the cluster has no Cilium network-policy support (the CiliumNetworkPolicy resource is missing), so the app cannot be fenced and was not started. " +
	"This is a problem on the platform operator's side, not in the app or the project: tell the platform operator to install Cilium; nothing needs changing in the app."

func isStatus(err error, status int) bool {
	var routeErr *RouteError
	return errors.As(err, &routeErr) && routeErr.Status == status
}

// ciliumMissing reads a failure that comes from a cluster with no Cilium
// network-policy kind: the raw text names a missing resource, not the cause.
func ciliumMissing(text string) bool {
	lower := strings.ToLower(text)
	if strings.Contains(lower, "cilium") && strings.Contains(lower, "no matches for kind") {
		return true
	}
	return strings.Contains(lower, "policy") && strings.Contains(lower, "could not find the requested resource")
}

// deployViaUpdate is Studio's own way to ship a new image where a deploy that
// names one is not served: store it on the app, then deploy the app as it
// stands. A 404 for an app that does not exist stays the original refusal.
func deployViaUpdate(ctx context.Context, c *call, path, image string, refused error) (map[string]any, error) {
	if _, err := patchApp(ctx, c, path, updateAppArgs{Image: image}); err != nil {
		if isStatus(err, http.StatusNotFound) {
			return nil, refused
		}
		return nil, err
	}
	var deploy map[string]any
	err := c.send(ctx, http.MethodPost, path+"/deploy", nil, nil, &deploy)
	return deploy, err
}

// deployFailure turns a refused deploy into what a person can act on.
func deployFailure(ctx context.Context, c *call, projectID, image string, err error) error {
	var routeErr *RouteError
	if !errors.As(err, &routeErr) {
		return err
	}
	if ciliumMissing(routeErr.Message) {
		return errors.New(ciliumMissingMessage)
	}
	if image != "" && routeErr.Status == http.StatusUnprocessableEntity && strings.Contains(routeErr.Message, "credential") {
		return registryAccessFailure(ctx, c, projectID, image, routeErr)
	}
	return err
}

// registryAccessFailure says whether the image's registry has a saved
// credential (names only: no route here returns a login) and where to add one.
func registryAccessFailure(ctx context.Context, c *call, projectID, image string, refused *RouteError) error {
	registry := apphost.ImageRegistry(image)
	var saved []struct {
		Registry string `json:"registry"`
	}
	if err := c.get(ctx, projectsAPI+projectID+"/registry-credentials/", nil, &saved); err != nil {
		return refused
	}
	for _, entry := range saved {
		if entry.Registry == registry {
			return fmt.Errorf("the credential saved for %s was refused by the registry: it may be wrong, expired or without access to this image. "+
				"Replace it in Studio, under Registry credentials: %s", registry, c.registryCredentialsURL(projectID))
		}
	}
	return fmt.Errorf("the project has no credential for %s, and the registry refused the image without one. "+
		"Add one in Studio, under Registry credentials (the login is entered there, not through MCP): %s", registry, c.registryCredentialsURL(projectID))
}

func (c *call) registryCredentialsURL(projectID string) string {
	return strings.TrimRight(c.settings.StudioURL, "/") + "/project/" + projectID + "/containers#registry-credentials"
}

// urlProbe is whether the app's public address answers right now.
type urlProbe struct {
	Answered bool   `json:"answered"`
	Status   int    `json:"status,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// probeAppURL asks the app's own address once, with a short timeout. An edge
// that answers 502, 503 or 504 has no ready app behind it yet.
func probeAppURL(ctx context.Context, address string) urlProbe {
	ctx, cancel := context.WithTimeout(ctx, urlProbeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil || (request.URL.Scheme != "http" && request.URL.Scheme != "https") {
		return urlProbe{Reason: "the app's address is not an http or https URL"}
	}
	client := &http.Client{CheckRedirect: noRedirects}
	answer, err := client.Do(request)
	if err != nil {
		return urlProbe{Reason: unreachableReason(err)}
	}
	defer answer.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(answer.Body, maxProbeBody))
	switch answer.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return urlProbe{Status: answer.StatusCode, Reason: fmt.Sprintf("the edge answered HTTP %d: no ready app behind the address yet", answer.StatusCode)}
	}
	return urlProbe{Answered: true, Status: answer.StatusCode}
}

func unreachableReason(err error) string {
	var verification *tls.CertificateVerificationError
	var unknown x509.UnknownAuthorityError
	var invalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var dns *net.DNSError
	var timeout net.Error
	switch {
	case errors.As(err, &verification), errors.As(err, &unknown), errors.As(err, &invalid), errors.As(err, &hostname):
		return "the HTTPS certificate is not accepted yet (certificate pending, or not trusted)"
	case errors.As(err, &dns):
		return "the address does not resolve yet (DNS pending)"
	case errors.As(err, &timeout) && timeout.Timeout(), errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("no answer within %s", urlProbeTimeout)
	}
	return "the address refused the connection or closed it"
}

type deployState struct {
	Status        string `json:"status"`
	FailureReason string `json:"failureReason"`
}

func readDeployState(deploy json.RawMessage) deployState {
	var state deployState
	_ = json.Unmarshal(deploy, &state)
	return state
}

func rollingOut(state deployState) bool {
	return state.Status == apphost.DeployStatusPending || state.Status == apphost.DeployStatusRolling
}

// describeDeploy adds what a client needs to tell a deploy's outcome from the
// app's stored status: a rollout under way, a failure's likely cause, and for
// a succeeded deploy whether the address answers.
func describeDeploy(ctx context.Context, c *call, path string, app appView, deploy json.RawMessage, out map[string]any) {
	state := readDeployState(deploy)
	switch {
	case rollingOut(state):
		out["state"] = stateRollingOut
	case state.Status == apphost.DeployStatusFailed:
		out["state"] = "failed"
		if ciliumMissing(state.FailureReason) {
			out["failureHint"] = ciliumMissingMessage
		}
	case state.Status == apphost.DeployStatusSucceeded:
		addCertificate(ctx, c, path, app, deploy, out)
		describeSucceeded(ctx, app, out)
	default:
		out["state"] = state.Status
	}
}

func describeSucceeded(ctx context.Context, app appView, out map[string]any) {
	out["state"] = apphost.DeployStatusSucceeded
	if app.URL == "" {
		return
	}
	probe := probeAppURL(ctx, app.URL)
	if !probe.Answered {
		if https, _ := out["https"].(map[string]any); https != nil && https["status"] == "certificate_pending" {
			probe.Reason = "the HTTPS certificate is still being issued (cert pending): " + probe.Reason
		}
		out["state"] = stateNotAnswered
	}
	out["urlProbe"] = probe
}
