package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

const (
	flowRegister  = "register"
	flowLogin     = "login"
	maxFlowSecret = 256
	maxFlowEmail  = 254
	maxFlowName   = 128
)

type authFlowArgs struct {
	projectArg
	Flow     string `json:"flow" jsonschema:"register (creates a test user) or login (password sign-in of an existing user)"`
	Email    string `json:"email" jsonschema:"the test user's email; use a throwaway test account"`
	Password string `json:"password" jsonschema:"the test user's password; sent once to the project's auth API and never stored or shown again"`
	FullName string `json:"full_name,omitempty" jsonschema:"register only: the user's full name"`
	Origin   string `json:"origin,omitempty" jsonschema:"the page's origin: the browser's CORS preflight for this call is checked too"`
}

const authFlowDescription = "Try the sign-up or sign-in a page offers, against the project's own auth API, with a test account you give: " +
	"it reports the status, whether tokens were issued (never their values), who the user is and the userId and role claims, " +
	"and with origin whether a browser page there is allowed by CORS. The password and the tokens are not stored or shown. " +
	"Use a throwaway test user; the end user's id type matters for permissions (see get_project_info)."

func authFlowTools() []entry {
	return []entry{
		tool("test_auth_flow", authFlowDescription, writeTool, testAuthFlow),
		tool("test_auth_flow", authFlowDescription+" On this read-only connection only login runs.", readOnlyVariant, testAuthFlow),
	}
}

// authFlowAnswer is what a flow reports: the auth API's answer with every
// token left out.
type authFlowAnswer struct {
	AccessToken               string `json:"accessToken"`
	EmailVerificationRequired *bool  `json:"emailVerificationRequired"`
	Message                   string `json:"message"`
	Error                     string `json:"error"`
	Code                      string `json:"code"`
	User                      *struct {
		ID       any    `json:"id"`
		Email    string `json:"email"`
		FullName string `json:"fullName"`
	} `json:"user"`
}

func testAuthFlow(ctx context.Context, c *call, in authFlowArgs) (any, error) {
	projectID, err := c.useProject(in.ProjectID)
	if err != nil {
		return nil, err
	}
	origin, err := validAuthFlow(c.caller.ReadOnly, in)
	if err != nil {
		return nil, err
	}
	base, err := dataPlaneBase(c.settings)
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
	target := endpoints(base, info.OrgSlug, projectID)["auth"]
	if in.Flow == flowRegister {
		target += "/register"
	} else {
		target += "/token"
	}
	// A browser asks first, so the preflight goes before the call itself.
	var cors map[string]any
	if origin != "" {
		cors = flowPreflight(ctx, c, client, projectID, target, origin)
	}
	out, err := sendAuthFlow(ctx, client, target, origin, in)
	if err != nil {
		return out, err
	}
	if cors != nil {
		out["cors"] = cors
	}
	return out, nil
}

func flowPreflight(ctx context.Context, c *call, client *http.Client, projectID, target, origin string) map[string]any {
	preflight := sendPreflight(ctx, client, target, origin, http.MethodPost, "content-type")
	cors := map[string]any{"request": preflight}
	if !preflight.Allowed {
		cors["blocked"] = blockedAdvice(origin, false)
		adviseOnBlock(ctx, c, projectID, origin, cors)
	}
	return cors
}

// validAuthFlow checks every input before anything is sent. Registering
// creates a user, so a read-only connection may only log in.
func validAuthFlow(readOnly bool, in authFlowArgs) (string, error) {
	if in.Flow != flowRegister && in.Flow != flowLogin {
		return "", errors.New("flow must be register or login")
	}
	if readOnly && in.Flow == flowRegister {
		return "", errors.New("this connection is read-only: register creates a user, only login runs")
	}
	if in.Password == "" || len(in.Password) > maxFlowSecret {
		return "", fmt.Errorf("password is required and at most %d characters", maxFlowSecret)
	}
	at := strings.Index(in.Email, "@")
	if at < 1 || at == len(in.Email)-1 || len(in.Email) > maxFlowEmail || strings.ContainsAny(in.Email, " \r\n\t") {
		return "", errors.New("email must be one email address")
	}
	if len(in.FullName) > maxFlowName || strings.ContainsAny(in.FullName, "\r\n") {
		return "", fmt.Errorf("full_name must be one line of at most %d characters", maxFlowName)
	}
	return probeOrigin(in.Origin)
}

func sendAuthFlow(ctx context.Context, client *http.Client, target, origin string, in authFlowArgs) (map[string]any, error) {
	payload := map[string]string{"email": in.Email, "password": in.Password}
	if in.Flow == flowRegister {
		payload["fullName"] = in.FullName
	} else {
		payload["grant_type"] = "password"
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		log.Printf("mcp: auth flow request: %v", err)
		return nil, errProbeUnanswered
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	answer, err := client.Do(request)
	if err != nil {
		log.Printf("mcp: auth flow: %v", err)
		return nil, errors.New("the project's auth API did not answer")
	}
	defer answer.Body.Close()
	var reply authFlowAnswer
	if err := json.NewDecoder(io.LimitReader(answer.Body, maxProbeBody)).Decode(&reply); err != nil && !errors.Is(err, io.EOF) {
		reply = authFlowAnswer{}
	}
	return summarizeAuthFlow(in, request.URL.Path, answer.StatusCode, reply), nil
}

// summarizeAuthFlow keeps the facts a developer needs and drops everything
// secret: tokens are read only for their claims, and the password is cut out
// of any text the API sent back.
func summarizeAuthFlow(in authFlowArgs, path string, status int, reply authFlowAnswer) map[string]any {
	out := map[string]any{"flow": in.Flow, "request": "POST " + path, "status": status, "tokensIssued": reply.AccessToken != ""}
	switch {
	case reply.AccessToken != "" && in.Flow == flowRegister:
		out["outcome"] = "registered"
	case reply.AccessToken != "":
		out["outcome"] = "signed_in"
	case reply.EmailVerificationRequired != nil && *reply.EmailVerificationRequired:
		out["outcome"] = "verification_required"
	case status >= 200 && status < 300:
		out["outcome"] = "answered"
	default:
		out["outcome"] = "rejected"
	}
	if reply.User != nil {
		out["user"] = map[string]any{"id": reply.User.ID, "email": reply.User.Email, "fullName": reply.User.FullName}
	}
	if claims := tokenClaims(reply.AccessToken); claims != nil {
		out["claims"] = claims
		out["note"] = "userId is a " + claimKind(claims["userId"]) + " claim; see get_project_info for how a permission compares it with an owner column."
	}
	for key, text := range map[string]string{"message": reply.Message, "error": reply.Error, "code": reply.Code} {
		if text != "" {
			out[key] = strings.ReplaceAll(text, in.Password, "[redacted]")
		}
	}
	return out
}

func claimKind(value any) string {
	if _, isString := value.(string); isString {
		return "string"
	}
	return "non-string"
}

// tokenClaims reads, without verifying, the identity claims of an access token.
func tokenClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	shown := map[string]any{}
	for _, name := range []string{"userId", "role", "projectId"} {
		if value, ok := claims[name]; ok {
			shown[name] = value
		}
	}
	return shown
}
