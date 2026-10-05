// Package mcpserver is the hosted MCP endpoint for coding tools (ADR 0037,
// decision 6). It is a door, not a second API: every tool is an in-process
// request through the same router Studio calls, carrying the caller's own
// credential, so route authorization, project access, token scopes and
// project activity apply unchanged.
package mcpserver

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/clientaddr"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

var validID = regexp.MustCompile(`^[a-zA-Z0-9_\-]{1,64}$`)

// Caller is who a connection acts as. Token is a narrowed copy of the
// credential that authenticated the request; the stored token is never
// changed.
type Caller struct {
	User       *domain.User
	Token      *domain.AccessToken
	ReadOnly   bool
	Project    string
	ClientAddr string
}

// refusal is a connection the endpoint turns away before speaking MCP.
type refusal struct {
	status  int
	message string
}

// connect resolves the caller of an MCP request. read_only and project only
// narrow what the token allows: a read token stays read-only, and a token
// bound to one project cannot be pointed at another.
func connect(r *http.Request) (Caller, *refusal) {
	user, token := auth.GetUser(r.Context()), auth.GetToken(r.Context())
	if user == nil || token == nil {
		return Caller{}, &refusal{http.StatusUnauthorized, "authentication required"}
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return Caller{}, &refusal{http.StatusUnauthorized, "send a personal access token in the Authorization header"}
	}
	if auth.IsSessionToken(token) || auth.IsCapabilityToken(token) {
		return Caller{}, &refusal{http.StatusForbidden, "MCP takes a personal access token, not a sign-in session or a service token"}
	}
	readOnly, err := readOnlyParam(r.URL.Query().Get("read_only"))
	if err != nil {
		return Caller{}, &refusal{http.StatusBadRequest, err.Error()}
	}
	project := r.URL.Query().Get("project")
	if project != "" && !validID.MatchString(project) {
		return Caller{}, &refusal{http.StatusBadRequest, "project must be a project id"}
	}
	if project != "" && token.ProjectID != "" && project != token.ProjectID {
		return Caller{}, &refusal{http.StatusForbidden, "this token is bound to another project"}
	}
	narrowed := *token
	if readOnly || !auth.TokenAllowsMethod(token, http.MethodPost) {
		readOnly = true
		narrowed.Scopes = auth.ScopeRead
	}
	if project != "" {
		narrowed.ProjectID = project
	}
	return Caller{
		User: user, Token: &narrowed, ReadOnly: readOnly,
		Project: narrowed.ProjectID, ClientAddr: clientaddr.FromRequest(r),
	}, nil
}

func readOnlyParam(raw string) (bool, error) {
	switch raw {
	case "", "false":
		return false, nil
	case "true":
		return true, nil
	}
	return false, fmt.Errorf("read_only must be true or false")
}

// resolveProject picks the project a tool acts on: the connection's own when
// it is bound to one, otherwise the one the tool call names.
func (c Caller) resolveProject(requested string) (string, error) {
	if requested != "" && !validID.MatchString(requested) {
		return "", fmt.Errorf("project_id must be a project id")
	}
	if c.Project != "" {
		if requested != "" && requested != c.Project {
			return "", fmt.Errorf("this connection is limited to project %s", c.Project)
		}
		return c.Project, nil
	}
	if requested == "" {
		return "", fmt.Errorf("project_id is required: call list_projects to find it")
	}
	return requested, nil
}
