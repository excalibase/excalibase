package main

import (
	"context"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/go-chi/chi/v5"
)

// EXC-845: writes outside a project mount must be decided too. A route that
// names an org is gated like a project write; any other write must be listed
// below with the reason it needs no gate, so a new route fails here until
// someone decides.

var orgScopedPrefix = "/api/orgs/{orgId}"

// notOrgScoped are the writes that need no suspended-org gate, each with why.
var notOrgScoped = map[string]string{
	"POST /api/credits/terms":                          "the caller's own acceptance of the credit terms; no org, and a suspended account is refused on every request",
	"POST /api/auth/login":                             "no org in the request; a suspended account is refused at login",
	"POST /api/auth/logout":                            "ends the caller's own session",
	"POST /api/auth/register":                          "creates an account, no org",
	"POST /api/auth/tokens/":                           "the caller's own credential; a suspended account is refused on every request",
	"DELETE /api/auth/tokens/{tokenHash}":              "revokes the caller's own credential",
	"POST /api/auth/tokens/{tokenHash}/rotate":         "the caller's own credential",
	"POST /api/auth/users/":                            "platform admin creates a user",
	"DELETE /api/auth/users/{userId}":                  "platform admin only",
	"POST /api/auth/users/{userId}/verify-email":       "platform admin only",
	"POST /api/email/reset/confirm":                    "own account, no org",
	"POST /api/email/reset/send":                       "own account, no org",
	"POST /api/email/verify/confirm":                   "own account, no org",
	"POST /api/email/verify/resend":                    "own account, no org",
	"POST /api/email/verify/send":                      "own account, no org",
	"POST /api/orgs/invites/accept":                    "refused in the handler for a suspended org (org_invite_org_suspended_test)",
	"POST /api/provision/":                             "refused in the service for a suspended org (provisioning guard)",
	"POST /api/provision/estimate":                     "computes a price, starts nothing",
	"POST /api/sites/github/webhook":                   "signed webhook; the builds it queues are guarded at queue and run time",
	"POST /internal/log-forwarder/ack":                 "service identity, internal listener",
	"POST /internal/log-forwarder/pull":                "service identity, internal listener",
	"POST /internal/registry/token":                    "service identity, internal listener",
	"POST /internal/runtime/functions/{fnId}/metadata": "function runtime callback",
	"POST /mcp":                           "dispatches into this router as the caller; every tool meets these gates",
	"POST /metrics":                       "scrape endpoint",
	"PUT /api/parameter-groups/{name}":    "platform-wide configuration, platform admin only",
	"POST /api/parameter-groups/":         "platform-wide configuration, platform admin only",
	"DELETE /api/parameter-groups/{name}": "platform-wide configuration, platform admin only",
}

func writeMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func exemptByPrefix(pattern string) bool {
	if pattern == "/mcp" || pattern == "/metrics" {
		return true // /mcp dispatches into this router as the caller; /metrics is a scrape
	}
	for _, prefix := range []string{"/api/admin/", "/functions/v1/", "/internal/storage/", "/internal/invoke/", "/internal/runtime/projects/"} {
		if strings.HasPrefix(pattern, prefix) {
			return true
		}
	}
	return false
}

func orgConcretePath(pattern string) string {
	return concretePath(strings.ReplaceAll(pattern, "{orgId}", matrixOrgA))
}

func TestEveryOrgWriteIsGatedOrDecided(t *testing.T) {
	suspended, token := suspendedMatrix(t, true)
	unsuspended, _ := suspendedMatrix(t, false)
	orgWrites := 0
	walk := func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !writeMethod(method) || exemptByPrefix(pattern) {
			return nil
		}
		key := method + " " + pattern
		switch {
		case strings.HasPrefix(pattern, orgScopedPrefix):
			orgWrites++
			checkOrgWrite(t, suspended, unsuspended, token, method, pattern)
		case strings.Contains(pattern, "{projectId}") && !isPublicProjectRoute(pattern):
		case notOrgScoped[key] != "":
		default:
			t.Errorf("%s has no suspended-org gate and no entry in notOrgScoped: decide, then gate it or list it with the reason", key)
		}
		return nil
	}
	if err := chi.Walk(suspended, walk); err != nil {
		t.Fatal(err)
	}
	if orgWrites < 6 {
		t.Fatalf("walked only %d org writes; the router lost mounts", orgWrites)
	}
}

func checkOrgWrite(t *testing.T, suspended, unsuspended http.Handler, token, method, pattern string) {
	t.Helper()
	path := orgConcretePath(pattern)
	code, body := suspendedRequest(suspended, method, path, token)
	refused := code == http.StatusForbidden && strings.Contains(body, orgguard.Message)
	switch {
	case suspendedMayWrite(method, path) && refused:
		t.Errorf("%s %s: refused a write the suspended may make", method, pattern)
	case !suspendedMayWrite(method, path) && !refused:
		t.Errorf("%s %s: got %d %q for a suspended owner's org, want 403 %q", method, pattern, code, body, orgguard.Message)
	}
	if code, body := suspendedRequest(unsuspended, method, path, token); strings.Contains(body, orgguard.Message) {
		t.Errorf("%s %s: refused an org in good standing: %d %q", method, pattern, code, body)
	}
}

func TestSuspendedOrgIsNotRevealedToANonMember(t *testing.T) {
	suspended, who := suspendedMatrixWho(t, true)
	code, body := suspendedRequest(suspended, http.MethodPatch, "/api/orgs/"+matrixOrgA+"/", who[callerOtherOrg])
	if code != http.StatusNotFound || strings.Contains(body, orgguard.Message) {
		t.Fatalf("a non-member got %d %q, want the plain 404", code, body)
	}
}

// goodStandingGuard is the production guard over a world with no suspension.
func goodStandingGuard(instances storage.InstanceStore) *orgguard.Guard {
	return orgguard.New(instances, func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil })
}
