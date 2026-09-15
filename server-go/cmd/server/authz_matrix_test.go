package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

// EXC-323: every project-scoped route must bind the path project to the
// caller before its handler runs. These tests drive the REAL router built by
// buildRouter (the production mounts) with fake stores, so a route mounted
// without the gate — or with the gate above the {projectId} segment where it
// is a no-op — fails here.

const (
	matrixProjectA = "proj-a"
	matrixProjectB = "proj-b"
	matrixOrgA     = "org-a"
	matrixOrgB     = "org-b"
	matrixDevID    = "dev-a"
	matrixOtherID  = "member-b"

	callerAnonymous      = "anonymous"
	callerDeveloper      = "developer"
	callerOtherOrg       = "otherOrg"
	callerBoundElsewhere = "boundElsewhere"
	callerReadOnly       = "readOnly"
)

// callers are the principals of the authz matrix, keyed by name; the value
// is the raw bearer token to send ("" = anonymous).
type callers map[string]string

// fakePlatform is the slice of the platform store the router needs.
type fakePlatform struct {
	*fakestore.Orgs
	*fakestore.Tokens
}

// matrixRouter builds the production router over fake stores and returns the
// bearer tokens of each caller class.
func matrixRouter(t *testing.T) (http.Handler, callers) {
	t.Helper()
	instances := fakestore.NewInstances()
	instances.Save(&domain.DatabaseInstance{ProjectID: matrixProjectA, OrgID: matrixOrgA, Status: "ACTIVE"})
	instances.Save(&domain.DatabaseInstance{ProjectID: matrixProjectB, OrgID: matrixOrgB, Status: "ACTIVE"})

	platform := &fakePlatform{Orgs: fakestore.NewOrgs(), Tokens: fakestore.NewTokens()}
	platform.AddMember(matrixOrgA, matrixDevID, domain.OrgRoleDeveloper)
	platform.AddMember(matrixOrgB, matrixOtherID, domain.OrgRoleDeveloper)
	platform.Users[matrixDevID] = &domain.User{ID: matrixDevID, Role: "user", Active: true}
	platform.Users[matrixOtherID] = &domain.User{ID: matrixOtherID, Role: "user", Active: true}

	who := callers{callerAnonymous: ""}
	issue := func(name, userID string, tok domain.AccessToken) {
		raw := testutil.FixtureToken(name)
		tok.TokenHash = auth.HashToken(raw)
		tok.UserID = userID
		platform.ByHash[tok.TokenHash] = &tok
		who[name] = raw
	}
	issue(callerDeveloper, matrixDevID, domain.AccessToken{Scopes: auth.ScopeSession})
	issue(callerOtherOrg, matrixOtherID, domain.AccessToken{Scopes: auth.ScopeSession})
	issue(callerBoundElsewhere, matrixDevID, domain.AccessToken{ProjectID: matrixProjectB})
	issue(callerReadOnly, matrixDevID, domain.AccessToken{ProjectID: matrixProjectA, Scopes: auth.ScopeRead})

	cfg := config.AppConfig{DeploymentMode: "selfhosted"}
	return buildRouter(cfg, platform, instances, matrixDeps(t, instances)), who
}

// matrixDeps wires the handlers whose registration or reachable path the
// matrix needs. Handlers left nil register fine (method values) and, once the
// gate lets a request through, fail inside the handler — which is still
// distinguishable from a gate refusal.
func matrixDeps(t *testing.T, instances *fakestore.Instances) *handlerDeps {
	t.Helper()
	dir := t.TempDir()
	mock := k8s.NewMockClient()
	provSvc := service.NewProvisioningService(instances, provisioner.NewFactory(), mock)
	return &handlerDeps{
		provHandler:      handler.NewProvisioningHandler(provSvc, nil),
		metricsHandler:   handler.NewMetricsHandler(service.NewMetricsService(instances, mock, dir)),
		backupHandler:    handler.NewBackupHandler(service.NewBackupService(instances, mock, dir, nil)),
		perfHandler:      handler.NewPerformanceHandler(service.NewPerformanceService(instances, mock)),
		auditHandler:     handler.NewAuditHandler(service.NewAuditService(instances, mock)),
		snapshotHandler:  handler.NewSnapshotHandler(service.NewSnapshotService(instances, mock, dir)),
		migrationHandler: handler.NewMigrationHandler(service.NewMigrationService(instances, nil, dir)),
		alertHandler:     handler.NewAlertHandler(service.NewAlertingService(dir)),
		setupHandler:     handler.NewSetupHandler(service.NewOperatorSetupService(mock)),
		rlUnauth:         custommw.RateLimit(custommw.PerIP, 1000, time.Minute),
		rlAuthed:         custommw.RateLimit(custommw.PerUser, 1000, time.Minute),
		rlDataPlane:      custommw.RateLimit(custommw.PerProjectAndUser, 1000, time.Second),
	}
}

func matrixRequest(router http.Handler, method, path, token string) int {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w.Code
}

// gateRefusal reports whether the status is one the project gate emits.
func gateRefusal(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden ||
		code == http.StatusNotFound || code == http.StatusMethodNotAllowed
}

func isMutating(method string) bool {
	return method != http.MethodGet && method != http.MethodHead
}

// projectRoute is one representative endpoint per route group, with the org
// role the mount requires beyond membership.
type projectRoute struct {
	group, method, path, minRole string
}

var projectRoutes = []projectRoute{
	{"provision status", http.MethodGet, "/api/provision/proj-a/", domain.OrgRoleViewer},
	{"provision credentials", http.MethodGet, "/api/provision/proj-a/credentials", domain.OrgRoleAdmin},
	{"provision delete", http.MethodDelete, "/api/provision/proj-a/", domain.OrgRoleAdmin},
	{"provision pause", http.MethodPost, "/api/provision/proj-a/pause", domain.OrgRoleAdmin},
	{"provision resume", http.MethodPost, "/api/provision/proj-a/resume", domain.OrgRoleAdmin},
	{"backup list", http.MethodGet, "/api/provision/proj-a/backup/list", domain.OrgRoleAdmin},
	{"backup trigger", http.MethodPost, "/api/provision/proj-a/backup/trigger", domain.OrgRoleAdmin},
	{"migrations", http.MethodGet, "/api/provision/proj-a/migrations/", domain.OrgRoleDeveloper},
	{"rls policies", http.MethodGet, "/api/provision/proj-a/rls-policies/", domain.OrgRoleDeveloper},
	{"schema browse", http.MethodGet, "/api/schema/proj-a/tables", domain.OrgRoleViewer},
	{"schema ddl", http.MethodPost, "/api/schema/proj-a/ddl", domain.OrgRoleDeveloper},
	{"functions list", http.MethodGet, "/api/projects/proj-a/functions/", domain.OrgRoleViewer},
	{"functions deploy", http.MethodPost, "/api/projects/proj-a/functions/", domain.OrgRoleDeveloper},
	{"functions egress read", http.MethodGet, "/api/projects/proj-a/functions/egress", domain.OrgRoleDeveloper},
	{"functions egress write", http.MethodPut, "/api/projects/proj-a/functions/egress", domain.OrgRoleDeveloper},
	{"schema apply", http.MethodPost, "/api/projects/proj-a/schema/apply", domain.OrgRoleDeveloper},
	{"project info", http.MethodGet, "/api/projects/proj-a/info/", domain.OrgRoleViewer},
	{"realtime tables", http.MethodGet, "/api/projects/proj-a/realtime/tables", domain.OrgRoleViewer},
	{"realtime enable-all", http.MethodPost, "/api/projects/proj-a/realtime/enable-all", domain.OrgRoleViewer},
	{"project alerts", http.MethodGet, "/api/alerts/project/proj-a", domain.OrgRoleViewer},
}

// expectedFor derives the matrix cell: anonymous 401; anyone the project is
// not visible to 404; a caller who can see it but lacks role or scope 403;
// otherwise the request must reach the handler.
func expectedFor(caller string, route projectRoute) (int, bool) {
	adminOnly := route.minRole == domain.OrgRoleAdmin
	switch caller {
	case callerAnonymous:
		return http.StatusUnauthorized, true
	case callerOtherOrg, callerBoundElsewhere:
		return http.StatusNotFound, true
	case callerReadOnly:
		if isMutating(route.method) || adminOnly {
			return http.StatusForbidden, true
		}
	case callerDeveloper:
		if adminOnly {
			return http.StatusForbidden, true
		}
	}
	return 0, false
}

func TestProjectRouteAuthzMatrix(t *testing.T) {
	router, who := matrixRouter(t)
	for _, route := range projectRoutes {
		for caller, token := range who {
			t.Run(route.group+"/"+caller, func(t *testing.T) {
				code := matrixRequest(router, route.method, route.path, token)
				want, refused := expectedFor(caller, route)
				if refused && code != want {
					t.Fatalf("%s %s as %s: got %d want %d", route.method, route.path, caller, code, want)
				}
				if !refused && gateRefusal(code) {
					t.Fatalf("%s %s as %s: gate refused with %d, expected the handler to run", route.method, route.path, caller, code)
				}
			})
		}
	}
}

// publicProjectPrefixes carry a {projectId} but are not Studio/PAT routes:
// end-user function invocation, runtime-to-control-plane callbacks and public
// object reads authenticate their own way. Platform-admin and org routes keep
// their own gates and are pinned by their own tests.
var publicProjectPrefixes = []string{"/functions/v1/", "/internal/", "/storage/v1/", "/api/admin/", "/api/orgs/"}

func isPublicProjectRoute(pattern string) bool {
	for _, prefix := range publicProjectPrefixes {
		if strings.HasPrefix(pattern, prefix) {
			return true
		}
	}
	return false
}

// concretePath turns a chi pattern into a request path aimed at proj-a.
func concretePath(pattern string) string {
	path := strings.ReplaceAll(pattern, "{projectId}", matrixProjectA)
	path = strings.ReplaceAll(path, "*", "x")
	for strings.Contains(path, "{") {
		start := strings.Index(path, "{")
		end := strings.Index(path, "}")
		path = path[:start] + "x" + path[end+1:]
	}
	return path
}

// TestEveryProjectRouteIsGated walks the real router: every registered route
// whose pattern names {projectId} (outside the public prefixes) must refuse an
// anonymous caller with 401 and a member of another org with 404.
func TestEveryProjectRouteIsGated(t *testing.T) {
	router, who := matrixRouter(t)
	mux, ok := router.(*chi.Mux)
	if !ok {
		t.Fatal("buildRouter must return a *chi.Mux")
	}
	seen := 0
	walk := func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.Contains(pattern, "{projectId}") || isPublicProjectRoute(pattern) {
			return nil
		}
		seen++
		path := concretePath(pattern)
		if code := matrixRequest(router, method, path, who[callerAnonymous]); code != http.StatusUnauthorized {
			t.Errorf("%s %s: anonymous got %d want 401", method, pattern, code)
		}
		if code := matrixRequest(router, method, path, who[callerOtherOrg]); code != http.StatusNotFound {
			t.Errorf("%s %s: other-org member got %d want 404", method, pattern, code)
		}
		return nil
	}
	if err := chi.Walk(mux, walk); err != nil {
		t.Fatal(err)
	}
	if seen < len(projectRoutes) {
		t.Fatalf("walked only %d project routes; the router lost mounts", seen)
	}
}
