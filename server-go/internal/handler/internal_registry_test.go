package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type fakeRotator struct {
	rotated []string
	err     error
}

func (f *fakeRotator) RotatePull(projectID string) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.rotated = append(f.rotated, projectID)
	return len(f.rotated), nil
}

func rotateRequest(h *InternalRegistryHandler) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/internal-registry", h.Routes)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/projects/proj-a/internal-registry/pull-credential/rotate", nil))
	return rec
}

func TestInternalRegistryHandler_RotatesTheProjectsPullCredential(t *testing.T) {
	rotator := &fakeRotator{}
	rec := rotateRequest(NewInternalRegistryHandler(rotator))
	if rec.Code != http.StatusOK || len(rotator.rotated) != 1 || rotator.rotated[0] != "proj-a" ||
		!strings.Contains(rec.Body.String(), `"generation":1`) || !strings.Contains(rec.Body.String(), "deploy") {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, rotator.rotated)
	}
	rotator.err = errors.New("vault sealed at /projects/proj-a")
	rec = rotateRequest(NewInternalRegistryHandler(rotator))
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "vault") {
		t.Fatalf("a store failure: %d %s", rec.Code, rec.Body)
	}
	if rec := rotateRequest(NewInternalRegistryHandler(nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("no registry: %d", rec.Code)
	}
}
