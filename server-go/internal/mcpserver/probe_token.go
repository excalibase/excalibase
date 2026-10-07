package mcpserver

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

var errNotEndUserToken = errors.New("access_token must be an access token a sign-in to this project answered (a JWT whose projectId is this project); " +
	"Studio access tokens, secret keys and service tokens are never used")

// endUserToken reads, without verifying, the claims of an access token and
// accepts only one the project's sign-in issued to an end user. The engine
// verifies the signature; this keeps any other credential out of the probe.
func endUserToken(token, projectID string) error {
	if len(token) > maxProbeToken || !probeJWT.MatchString(token) {
		return errNotEndUserToken
	}
	parts := strings.Split(token, ".")
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return errNotEndUserToken
	}
	var claims struct {
		ProjectID string `json:"projectId"`
		Role      string `json:"role"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.ProjectID != projectID || claims.Role == "" || claims.Role == "service" {
		return errNotEndUserToken
	}
	return nil
}
