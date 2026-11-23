package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// A suspension answers 403 with the plain message; a suspension that could not
// be read answers 503, never a 500 that reads as a platform fault.
func TestWriteOrgGuardError_AnswersByCause(t *testing.T) {
	cases := []struct {
		name string
		err  error
		code int
		body string
	}{
		{"suspended", fmt.Errorf("wrapped: %w", orgguard.ErrOrgSuspended), http.StatusForbidden, orgguard.Message},
		{"unreadable", fmt.Errorf("%w: %w", orgguard.ErrCheckUnavailable, errors.New("db down")), http.StatusServiceUnavailable, orgguard.ErrCheckUnavailable.Error()},
	}
	for _, tc := range cases {
		w := httptest.NewRecorder()
		writeOrgGuardError(w, tc.err)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.body) || strings.Contains(w.Body.String(), "db down") {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body.String())
		}
	}
}

func TestWriteSiteError_MapsTheSuspensionRefusals(t *testing.T) {
	for err, code := range map[error]int{
		orgguard.ErrOrgSuspended:                       http.StatusForbidden,
		fmt.Errorf("%w", orgguard.ErrCheckUnavailable): http.StatusServiceUnavailable,
	} {
		w := httptest.NewRecorder()
		writeSiteError(w, err)
		if w.Code != code {
			t.Errorf("%v: %d, want %d", err, w.Code, code)
		}
	}
}

func TestMinorUpgrade_RefusedWhileTheOrgOwnerIsSuspended(t *testing.T) {
	store, _ := storage.NewFileSystemStore(t.TempDir())
	mock := k8s.NewMockClient()
	svc := service.NewProvisioningService(store, provisioner.NewFactory(), mock)
	svc.SetOrgGuard(suspendedOwnerOf(store, "o"))
	h := NewProvisioningHandler(svc, nil)
	h.SetInstanceStore(store)
	r := chi.NewRouter()
	r.Route("/api/provision", func(r chi.Router) { h.Routes(r) })
	seedUpgradableProject(t, store, mock, activeK8sProject("up-1", "16"))

	w := postUpgrade(t, r, "up-1")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), orgguard.Message) {
		t.Fatalf("%d %s, want 403 with the suspension message", w.Code, w.Body.String())
	}

	// The same project with an unreadable suspension list answers 503.
	svc.SetOrgGuard(orgguard.New(store, func(context.Context) (map[string]bool, error) { return nil, errors.New("db down") }))
	if w := postUpgrade(t, r, "up-1"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreadable: %d %s, want 503", w.Code, w.Body.String())
	}
}

func TestWriteGitBuildErrors_MapTheSuspensionRefusals(t *testing.T) {
	for name, write := range map[string]func(http.ResponseWriter, error){"site": writeSiteGitError, "app": writeAppGitError} {
		w := httptest.NewRecorder()
		write(w, orgguard.ErrOrgSuspended)
		if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), orgguard.Message) {
			t.Errorf("%s: %d %s, want 403 with the message", name, w.Code, w.Body.String())
		}
		w = httptest.NewRecorder()
		write(w, fmt.Errorf("%w", orgguard.ErrCheckUnavailable))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d, want 503", name, w.Code)
		}
	}
}
