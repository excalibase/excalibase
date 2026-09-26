package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

const knownProject = "proj-known"

type noPolicies struct{}

func (noPolicies) ListRls(context.Context, string, string) ([]domain.Policy, error) {
	return []domain.Policy{}, nil
}
func (noPolicies) GetRls(context.Context, string, string) (*domain.Policy, error) {
	return nil, pgstore.ErrPolicyNotFound
}
func (noPolicies) UpsertRls(context.Context, *domain.Policy) error { return nil }
func (noPolicies) DeleteRls(context.Context, string, string) error { return nil }
func (noPolicies) ListColumn(context.Context, string, string) ([]domain.ColumnPolicy, error) {
	return []domain.ColumnPolicy{}, nil
}
func (noPolicies) GetColumn(context.Context, string, string) (*domain.ColumnPolicy, error) {
	return nil, pgstore.ErrPolicyNotFound
}
func (noPolicies) UpsertColumn(context.Context, *domain.ColumnPolicy) error { return nil }
func (noPolicies) DeleteColumn(context.Context, string, string) error       { return nil }

func policyLookupRouter(projects ProjectFinder) chi.Router {
	rls := NewRlsPolicyHandler(noPolicies{}, projects)
	grants := NewTableGrantHandler(newFakeGrantStore(), projects, true)
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}/rls-policies", func(r chi.Router) { rls.RlsRoutes(r) })
	r.Route("/api/provision/{projectId}/column-policies", func(r chi.Router) { rls.ColumnRoutes(r) })
	r.Route("/api/provision/{projectId}/table-grants", func(r chi.Router) { grants.Routes(r) })
	return r
}

func knownProjects() *fakestore.Instances {
	projects := fakestore.NewInstances()
	projects.Items[knownProject] = &domain.DatabaseInstance{ProjectID: knownProject, OrgID: "org-1"}
	return projects
}

var policyListPaths = []string{"rls-policies/", "column-policies/", "table-grants/"}

func getPolicyList(router http.Handler, projectID, list string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/provision/"+projectID+"/"+list, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestPolicyLists_UnknownProjectIsNotFound(t *testing.T) {
	router := policyLookupRouter(knownProjects())
	for _, list := range policyListPaths {
		w := getPolicyList(router, "proj-nonexistent", list)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s: want 404 for an unknown project, got %d body=%s", list, w.Code, w.Body.String())
		}
	}
}

func TestPolicyLists_KnownProjectWithoutPoliciesIsAnEmptyAnswer(t *testing.T) {
	router := policyLookupRouter(knownProjects())
	for _, list := range policyListPaths {
		w := getPolicyList(router, knownProject, list)
		if w.Code != http.StatusOK {
			t.Errorf("%s: want 200 for a known project, got %d body=%s", list, w.Code, w.Body.String())
		}
	}
}

func TestPolicyLists_LookupFailureIsNotAnEmptyAnswer(t *testing.T) {
	projects := knownProjects()
	projects.Err = errors.New("connection refused")
	router := policyLookupRouter(projects)
	for _, list := range policyListPaths {
		w := getPolicyList(router, knownProject, list)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s: want 500 when the project cannot be looked up, got %d", list, w.Code)
		}
	}
}

func TestPolicyLists_NoProjectSourceFailsClosed(t *testing.T) {
	router := policyLookupRouter(nil)
	for _, list := range policyListPaths {
		w := getPolicyList(router, knownProject, list)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("%s: want 500 without a project source, got %d", list, w.Code)
		}
	}
}
