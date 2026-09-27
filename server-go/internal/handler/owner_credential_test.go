package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

func TestCredentialsTheVaultCannotAnswerAreThePlatformsFault(t *testing.T) {
	r, store := setupTestRouter(t)
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "novault-proj", OrgID: "org1", Status: "ACTIVE", Username: "owner"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/provision/novault-proj/credentials", nil))

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", w.Code, w.Body.String())
	}
}

func TestAProjectResponseNeverCarriesTheOwnerPassword(t *testing.T) {
	r, store := setupTestRouter(t)
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "shown-proj", OrgID: "org1", Status: "ACTIVE", Username: "owner", Password: "owner-secret"}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/provision/shown-proj", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "owner-secret") {
		t.Errorf("the project response carries the owner password: %s", w.Body.String())
	}
}
