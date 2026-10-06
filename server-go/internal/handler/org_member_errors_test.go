package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func TestWriteOrgMemberChangeError(t *testing.T) {
	cases := map[error]int{
		fmt.Errorf("wrap: %w", storage.ErrOrgMemberNotFound): http.StatusNotFound,
		storage.ErrLastOwner:                 http.StatusConflict,
		errors.New("pq: connection refused"): http.StatusInternalServerError,
	}
	for err, want := range cases {
		w := httptest.NewRecorder()
		writeOrgMemberChangeError(w, err, "failed")
		if w.Code != want {
			t.Errorf("%v: got %d, want %d", err, w.Code, want)
		}
		if strings.Contains(w.Body.String(), "pq") {
			t.Errorf("%v: answer leaks the driver: %s", err, w.Body.String())
		}
	}
}

func TestWriteProjectMemberAddError(t *testing.T) {
	cases := map[error]int{
		storage.ErrProjectMemberExists:       http.StatusConflict,
		storage.ErrUserNotFound:              http.StatusNotFound,
		errors.New("pq: connection refused"): http.StatusInternalServerError,
	}
	for err, want := range cases {
		w := httptest.NewRecorder()
		writeProjectMemberAddError(w, err)
		if w.Code != want {
			t.Errorf("%v: got %d, want %d", err, w.Code, want)
		}
	}
}

func TestRequireOrgMember(t *testing.T) {
	orgs := fakestore.NewOrgs()
	orgs.AddOrg("org1", domain.Free)
	orgs.AddMember("org1", "bob", domain.OrgRoleDeveloper)
	h := NewOrgHandler(orgs, nil)
	req := httptest.NewRequest("POST", "/", nil)

	for userID, want := range map[string]int{"": http.StatusBadRequest, "nobody": http.StatusNotFound} {
		w := httptest.NewRecorder()
		if h.requireOrgMember(w, req, "org1", userID) || w.Code != want {
			t.Errorf("%q: got %d, want %d", userID, w.Code, want)
		}
	}
	if !h.requireOrgMember(httptest.NewRecorder(), req, "org1", "bob") {
		t.Error("an org member was refused")
	}

	orgs.Err = errors.New("pq: down")
	w := httptest.NewRecorder()
	if h.requireOrgMember(w, req, "org1", "bob") || w.Code != http.StatusInternalServerError {
		t.Errorf("store failure: got %d, want 500", w.Code)
	}
}
