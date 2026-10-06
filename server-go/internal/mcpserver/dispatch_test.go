package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

// noTokens is an ExtractAuth lookup that knows no credential, so any request
// reaching the gates with a header would be anonymous.
type noTokens struct{}

func (noTokens) FindByTokenHash(context.Context, string) (*domain.AccessToken, error) {
	return nil, nil
}
func (noTokens) FindUserByID(context.Context, string) (*domain.User, error) { return nil, nil }

// gatedRouter is the production gate stack in miniature: ExtractAuth,
// RequireAuth, project access and the developer rung for writes.
func gatedRouter(t *testing.T) http.Handler {
	t.Helper()
	instances := fakestore.NewInstances()
	instances.Items[testProjectA] = &domain.DatabaseInstance{ProjectID: testProjectA, OrgID: "org-a", Status: "ACTIVE"}
	instances.Items[testProjectB] = &domain.DatabaseInstance{ProjectID: testProjectB, OrgID: "org-a", Status: "ACTIVE"}
	orgs := fakestore.NewOrgs()
	orgs.AddMember("org-a", testUserID, domain.OrgRoleDeveloper)

	r := chi.NewRouter()
	r.Use(auth.ExtractAuth(noTokens{}))
	r.Route("/api/schema/{projectId}", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Use(custommw.TenantContext)
		r.Use(custommw.RequireProjectAccess(instances, orgs))
		r.Use(custommw.RequireProjectRoleForWrites(domain.OrgRoleDeveloper, instances, orgs))
		echo := func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"method": r.Method, "body": string(body), "remote": r.RemoteAddr,
				"authorization": r.Header.Get("Authorization"), "cookie": r.Header.Get("Cookie"),
			})
		}
		r.Get("/tables", echo)
		r.Post("/query", echo)
	})
	return r
}

func callerFor(token domain.AccessToken, readOnly bool) Caller {
	token.UserID = testUserID
	return Caller{
		User: &domain.User{ID: testUserID, Active: true}, Token: &token,
		ReadOnly: readOnly, Project: token.ProjectID, ClientAddr: "203.0.113.7",
	}
}

func TestDispatchCarriesTheCallersCredentialThroughTheGates(t *testing.T) {
	router := gatedRouter(t)
	d := dispatcher{router: router, caller: callerFor(domain.AccessToken{Scopes: auth.ScopeWrite}, false)}

	var echoed map[string]string
	if err := d.send(context.Background(), http.MethodPost, "/api/schema/"+testProjectA+"/query", nil,
		map[string]string{"query": "select 1"}, &echoed); err != nil {
		t.Fatalf("send: %v", err)
	}
	if echoed["method"] != http.MethodPost || !strings.Contains(echoed["body"], "select 1") {
		t.Errorf("handler saw %+v", echoed)
	}
	if echoed["authorization"] != "" || echoed["cookie"] != "" {
		t.Errorf("the internal request must carry no raw credential, saw %+v", echoed)
	}
	if !strings.HasPrefix(echoed["remote"], "203.0.113.7") {
		t.Errorf("client address not carried: %q", echoed["remote"])
	}
}

func TestDispatchReadOnlyCredentialIsRefusedWritesByTheRouter(t *testing.T) {
	router := gatedRouter(t)
	d := dispatcher{router: router, caller: callerFor(domain.AccessToken{Scopes: auth.ScopeRead}, true)}

	if err := d.send(context.Background(), http.MethodGet, "/api/schema/"+testProjectA+"/tables", nil, nil, nil); err != nil {
		t.Fatalf("a read must pass: %v", err)
	}
	err := d.send(context.Background(), http.MethodPost, "/api/schema/"+testProjectA+"/query", nil, map[string]string{"query": "delete"}, nil)
	var routeErr *RouteError
	if !errors.As(err, &routeErr) || routeErr.Status != http.StatusForbidden {
		t.Fatalf("err = %v, want a 403 route error", err)
	}
}

func TestDispatchBoundCredentialNeverReachesAnotherProject(t *testing.T) {
	router := gatedRouter(t)
	d := dispatcher{router: router, caller: callerFor(domain.AccessToken{Scopes: auth.ScopeWrite, ProjectID: testProjectA}, false)}

	err := d.send(context.Background(), http.MethodGet, "/api/schema/"+testProjectB+"/tables", nil, nil, nil)
	var routeErr *RouteError
	if !errors.As(err, &routeErr) || routeErr.Status != http.StatusNotFound {
		t.Fatalf("err = %v, want a 404 route error", err)
	}
}

func TestRouteErrorCarriesTheRoutesOwnMessage(t *testing.T) {
	r := chi.NewRouter()
	r.Get("/x", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"project has no database"}`))
	})
	d := dispatcher{router: r, caller: callerFor(domain.AccessToken{Scopes: auth.ScopeWrite}, false)}
	err := d.send(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	var routeErr *RouteError
	if !errors.As(err, &routeErr) || routeErr.Message != "project has no database" || routeErr.Status != http.StatusConflict {
		t.Fatalf("err = %#v", err)
	}
}
