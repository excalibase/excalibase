package middleware

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sites"
)

// A site deploy token (EXC-620) is default-deny, like a capability token: it
// may deploy its own site and read that site's deploys, and every other route
// refuses it. Past this gate it is its creator, bound to the site's project
// with the write scope, so the usual project and role gates still apply.

const errDeployTokenScope = "a deploy token may only deploy its own site and read its deploys"

// deployTokenRoute names a site's deploy upload and its deploy history.
var deployTokenRoute = regexp.MustCompile(`^/api/projects/([^/]+)/sites/([^/]+)/(deploy|deploys|deploys/[^/]+)$`)

// DeployTokenResolver answers the live deploy token a raw credential names, nil for none.
type DeployTokenResolver interface {
	ResolveDeployToken(ctx context.Context, raw string) (*sites.DeployToken, error)
	RecordDeployTokenUse(ctx context.Context, token *sites.DeployToken)
}

// deployTokenTarget answers the project and site a request a deploy token may
// make names; ok is false for any request no deploy token may make.
func deployTokenTarget(r *http.Request) (projectID, siteID string, ok bool) {
	// chi routes on the escaped path; refuse any path the gate could read differently.
	if r.URL.RawPath != "" || hasDotSegment(r.URL.Path) {
		return "", "", false
	}
	match := deployTokenRoute.FindStringSubmatch(r.URL.Path)
	if match == nil {
		return "", "", false
	}
	if match[3] == "deploy" && r.Method != http.MethodPost {
		return "", "", false
	}
	if match[3] != "deploy" && r.Method != http.MethodGet {
		return "", "", false
	}
	return match[1], match[2], true
}

// SiteDeployTokenGate authenticates a deploy token and refuses it on every
// route but its own. Mount it on the root router right after auth.ExtractAuth.
func SiteDeployTokenGate(tokens DeployTokenResolver, users auth.TokenLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !sites.IsDeployToken(raw) {
				next.ServeHTTP(w, r)
				return
			}
			// Nothing else authenticates this request: a deploy token never rides on a session.
			ctx := auth.SetToken(auth.SetUser(r.Context(), nil), nil)
			projectID, siteID, ok := deployTokenTarget(r)
			if !ok {
				writeGateError(w, http.StatusForbidden, errDeployTokenScope)
				return
			}
			if tokens == nil {
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			token, err := tokens.ResolveDeployToken(ctx, raw)
			if err != nil {
				log.Printf("deploy token: %v", err)
				writeGateError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if token == nil {
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if token.ProjectID != projectID || token.SiteID != siteID {
				writeGateError(w, http.StatusForbidden, errDeployTokenScope)
				return
			}
			tokens.RecordDeployTokenUse(ctx, token)
			ctx = sites.WithDeployToken(ctx, token)
			next.ServeHTTP(w, r.WithContext(asDeployTokenCreator(ctx, users, token)))
		})
	}
}

// asDeployTokenCreator is the request as the token's creator, narrowed to its
// project; an account gone or deactivated leaves it unauthenticated.
func asDeployTokenCreator(ctx context.Context, users auth.TokenLookup, token *sites.DeployToken) context.Context {
	user, err := users.FindUserByID(ctx, token.CreatedBy)
	if err != nil {
		log.Printf("deploy token %s: read its creator: %v", token.ID, err)
		return ctx
	}
	if user == nil || !user.Active {
		return ctx
	}
	ctx = auth.SetUser(ctx, user)
	return auth.SetToken(ctx, &domain.AccessToken{
		UserID: user.ID, Name: "deploy token " + token.Name, Scopes: auth.ScopeWrite, ProjectID: token.ProjectID,
	})
}

func writeGateError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
