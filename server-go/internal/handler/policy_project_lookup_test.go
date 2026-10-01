package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
	"github.com/go-chi/chi/v5"
)

const knownProject = "proj-known"

func documentLookupRouter(projects ProjectFinder) chi.Router {
	h := NewPermissionHandler(newFakePermissionStore(), projects, &fakeInspector{})
	r := chi.NewRouter()
	r.Route("/api/provision/{projectId}/permissions", h.PermissionRoutes)
	return r
}

func knownProjects() *fakestore.Instances {
	projects := fakestore.NewInstances()
	projects.Items[knownProject] = &domain.DatabaseInstance{ProjectID: knownProject, OrgID: "org-1"}
	return projects
}

func getPermissionDocument(router http.Handler, projectID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/provision/"+projectID+"/permissions/", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestPermissionDocument_KnownProjectWithoutPermissionsIsAnEmptyAnswer(t *testing.T) {
	w := getPermissionDocument(documentLookupRouter(knownProjects()), knownProject)
	if w.Code != http.StatusOK {
		t.Errorf("want 200 for a known project, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestPermissionDocument_UnknownProjectIsNotFound(t *testing.T) {
	w := getPermissionDocument(documentLookupRouter(knownProjects()), "proj-nonexistent")
	if w.Code != http.StatusNotFound {
		t.Errorf("want 404 for an unknown project, got %d body=%s", w.Code, w.Body.String())
	}
}

func TestPermissionDocument_LookupFailureIsNotAnEmptyAnswer(t *testing.T) {
	projects := knownProjects()
	projects.Err = errors.New("connection refused")
	w := getPermissionDocument(documentLookupRouter(projects), knownProject)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("want 500 when the project cannot be looked up, got %d", w.Code)
	}
}

func TestPermissionDocument_NoProjectSourceFailsClosed(t *testing.T) {
	w := getPermissionDocument(documentLookupRouter(nil), knownProject)
	if w.Code != http.StatusInternalServerError {
		t.Errorf("want 500 without a project source, got %d", w.Code)
	}
}
