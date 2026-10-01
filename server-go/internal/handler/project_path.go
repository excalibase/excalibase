package handler

import (
	"context"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/go-chi/chi/v5"
)

const (
	errNotFound    = "not found"
	errInvalidJSON = "invalid json"
)

// PolicyChangePublisher is implemented by whoever owns the NATS connection.
// Wired from cmd/server/main.go; nil-tolerant so unit tests that don't
// care about events can leave it unset.
type PolicyChangePublisher interface {
	PublishPolicyChange(ctx context.Context, evt domain.PolicyChangeEvent)
}

// ProjectFinder resolves a project row; a nil instance with no error means the
// project does not exist.
type ProjectFinder interface {
	FindByProjectID(projectID string) (*domain.DatabaseInstance, error)
}

// projectIDFromPath returns the validated projectId from the chi URL path,
// or writes a 400 + reports !ok.
func projectIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "projectId")
	if !isValidID(id) {
		httpError(w, "invalid projectId", http.StatusBadRequest)
		return "", false
	}
	return id, true
}

// knownProjectFromPath is projectIDFromPath for documents the data plane reads.
// Platform service tokens skip the membership lookup that would 404 an unknown
// project, and an empty document for one would read as "nothing permitted"
// rather than "no such project".
func knownProjectFromPath(w http.ResponseWriter, r *http.Request, projects ProjectFinder) (string, bool) {
	projectID, ok := projectIDFromPath(w, r)
	if !ok {
		return "", false
	}
	if projects == nil {
		httpError(w, "project lookup unavailable", http.StatusInternalServerError)
		return "", false
	}
	inst, err := projects.FindByProjectID(projectID)
	if err != nil {
		httpError(w, safeError(err), http.StatusInternalServerError)
		return "", false
	}
	if inst == nil {
		httpError(w, "project not found", http.StatusNotFound)
		return "", false
	}
	return projectID, true
}
