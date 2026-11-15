package middleware

import (
	"context"
	"database/sql/driver"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

type brokenInstances struct{ *fakestore.Instances }

func (brokenInstances) FindByProjectID(string) (*domain.DatabaseInstance, error) {
	return nil, context.DeadlineExceeded
}

// A database that cannot answer must not read as "the project does not exist":
// callers (the engine among them) act on a 404.
func TestRequireProjectAccess_DatabaseDownAnswers503Not404(t *testing.T) {
	_, orgs := storesWithMember("u1", "owner")
	handler := RequireProjectAccess(brokenInstances{fakestore.NewInstances()}, orgs)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run")
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, projectRequest(http.MethodGet, testProject, memberUser("u1"), nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestRequireProjectRole_DatabaseDownAnswers503(t *testing.T) {
	_, orgs := storesWithMember("u1", "owner")
	handler := RequireProjectRole("viewer", brokenInstances{fakestore.NewInstances()}, orgs)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, projectRequest(http.MethodGet, testProject, memberUser("u1"), nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

type failingDeployTokens struct{ err error }

func (f failingDeployTokens) ResolveDeployToken(context.Context, string) (*sites.DeployToken, error) {
	return nil, f.err
}
func (failingDeployTokens) RecordDeployTokenUse(context.Context, *sites.DeployToken) {}

func TestSiteDeployTokenGate_DatabaseDownAnswers503Other500(t *testing.T) {
	for err, want := range map[error]int{
		driver.ErrBadConn:        http.StatusServiceUnavailable,
		context.DeadlineExceeded: http.StatusServiceUnavailable,
		context.Canceled:         http.StatusServiceUnavailable,
		errBoom{}:                http.StatusInternalServerError,
	} {
		gate := SiteDeployTokenGate(failingDeployTokens{err}, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		req := httptest.NewRequest(http.MethodPost, "/api/projects/p1/sites/s1/deploy", nil)
		req.Header.Set("Authorization", "Bearer "+sites.DeployTokenPrefix+"abc")
		rec := httptest.NewRecorder()
		gate.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%v: status = %d, want %d", err, rec.Code, want)
		}
	}
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }
