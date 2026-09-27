package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/studiooauth"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

type ssoAudit struct{ entries []*domain.AuditEntry }

func (a *ssoAudit) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	a.entries = append(a.entries, entry)
	return nil
}

type ssoHarness struct {
	router chi.Router
	vault  ssoSecrets
	audit  *ssoAudit
	signIn *studiooauth.Service
}

func newSSOHarness(t *testing.T, secrets ssoSecrets) *ssoHarness {
	t.Helper()
	if secrets == nil {
		v, err := vault.NewWithStore(vault.NewMemoryStore())
		if err != nil {
			t.Fatalf("vault: %v", err)
		}
		if _, err := v.Init(1, 1); err != nil {
			t.Fatalf("vault init: %v", err)
		}
		secrets = v
	}
	h := &ssoHarness{vault: secrets, audit: &ssoAudit{}}
	h.signIn = studiooauth.New([]studiooauth.Provider{
		studiooauth.Google(studiooauth.GoogleEndpoints), studiooauth.GitHub(studiooauth.GitHubEndpoints),
	}, secrets, nil, testStudioURL, http.DefaultClient)
	handler := NewSSOProvidersHandler(h.signIn.Names(), secrets, testStudioURL, h.audit)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.SetUser(r.Context(), &domain.User{ID: "admin-1", Role: "platform_admin"})))
		})
	})
	r.Route("/api/admin/sso-providers", handler.Routes)
	h.router = r
	return h
}

type ssoProviderView struct {
	Provider        string `json:"provider"`
	Enabled         bool   `json:"enabled"`
	ClientID        string `json:"clientId"`
	ClientSecretSet bool   `json:"clientSecretSet"`
	CallbackURL     string `json:"callbackUrl"`
}

func (h *ssoHarness) list(t *testing.T) map[string]ssoProviderView {
	t.Helper()
	w := doRequest(h.router, "GET", "/api/admin/sso-providers/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Providers []ssoProviderView `json:"providers"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	views := map[string]ssoProviderView{}
	for _, v := range body.Providers {
		views[v.Provider] = v
	}
	return views
}

func TestSSOProvidersListShowsTheCallbackToRegister(t *testing.T) {
	h := newSSOHarness(t, nil)
	views := h.list(t)
	google := views["google"]
	if google.Enabled || google.ClientSecretSet || google.ClientID != "" ||
		google.CallbackURL != testStudioURL+"/api/auth/oauth/google/callback" {
		t.Fatalf("google: %+v", google)
	}
	if views["github"].CallbackURL != testStudioURL+"/api/auth/oauth/github/callback" {
		t.Fatalf("github: %+v", views["github"])
	}
}

// A saved provider is offered on the next sign-in, the secret is never sent
// back, and the audit says who changed what without the secret.
func TestSSOProviderSaveTakesEffectWithoutARestart(t *testing.T) {
	h := newSSOHarness(t, nil)
	w := doRequest(h.router, "PUT", "/api/admin/sso-providers/github",
		`{"enabled":true,"clientId":"gh-client","clientSecret":"gh-very-secret"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "gh-very-secret") {
		t.Fatal("the secret was returned")
	}
	if got := h.signIn.Configured(); !slices.Equal(got, []string{"github"}) {
		t.Fatalf("offered after save: %v", got)
	}
	stored, _ := h.vault.Get(studiooauth.SecretPath("github"))
	if stored["client_secret"] != "gh-very-secret" || stored["client_id"] != "gh-client" || stored["enabled"] != "true" {
		t.Fatalf("vault: %v", stored)
	}
	view := h.list(t)["github"]
	if !view.Enabled || !view.ClientSecretSet || view.ClientID != "gh-client" {
		t.Fatalf("view: %+v", view)
	}
	if strings.Contains(doRequest(h.router, "GET", "/api/admin/sso-providers/", "").Body.String(), "gh-very-secret") {
		t.Fatal("the list returned the secret")
	}
	entry := h.audit.entries[0]
	if entry.Action != "sso_provider.update" || entry.UserID != "admin-1" || entry.ResourceID != "github" ||
		strings.Contains(entry.Details, "gh-very-secret") || !strings.Contains(entry.Details, `"secretChanged":true`) {
		t.Fatalf("audit: %+v", entry)
	}
}

func TestSSOProviderKeepsTheStoredSecretWhenNoneIsSent(t *testing.T) {
	h := newSSOHarness(t, nil)
	doRequest(h.router, "PUT", "/api/admin/sso-providers/google", `{"enabled":true,"clientId":"g1","clientSecret":"s1"}`)
	if w := doRequest(h.router, "PUT", "/api/admin/sso-providers/google", `{"enabled":true,"clientId":"g2"}`); w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	stored, _ := h.vault.Get(studiooauth.SecretPath("google"))
	if stored["client_secret"] != "s1" || stored["client_id"] != "g2" {
		t.Fatalf("vault: %v", stored)
	}
	if !strings.Contains(h.audit.entries[1].Details, `"secretChanged":false`) {
		t.Fatalf("audit: %s", h.audit.entries[1].Details)
	}
}

func TestSSOProviderDisableStopsOfferingIt(t *testing.T) {
	h := newSSOHarness(t, nil)
	doRequest(h.router, "PUT", "/api/admin/sso-providers/google", `{"enabled":true,"clientId":"g1","clientSecret":"s1"}`)
	if w := doRequest(h.router, "PUT", "/api/admin/sso-providers/google", `{"enabled":false,"clientId":"g1"}`); w.Code != http.StatusOK {
		t.Fatalf("disable: %d", w.Code)
	}
	if got := h.signIn.Configured(); len(got) != 0 {
		t.Fatalf("still offered: %v", got)
	}
	if view := h.list(t)["google"]; view.Enabled || !view.ClientSecretSet {
		t.Fatalf("view: %+v", view)
	}
}

func TestSSOProviderRefusesAnIncompleteOrUnknownProvider(t *testing.T) {
	h := newSSOHarness(t, nil)
	cases := map[string]struct {
		path, body string
		want       int
	}{
		"enable without a secret": {"google", `{"enabled":true,"clientId":"g1"}`, http.StatusBadRequest},
		"enable without a client": {"google", `{"enabled":true,"clientSecret":"s"}`, http.StatusBadRequest},
		"client id with spaces":   {"google", `{"enabled":false,"clientId":"a b"}`, http.StatusBadRequest},
		"oversized secret":        {"google", `{"enabled":false,"clientSecret":"` + strings.Repeat("s", 600) + `"}`, http.StatusBadRequest},
		"malformed body":          {"google", `nope`, http.StatusBadRequest},
		"unknown provider":        {"gitlab", `{"enabled":false}`, http.StatusNotFound},
	}
	for name, tc := range cases {
		if w := doRequest(h.router, "PUT", "/api/admin/sso-providers/"+tc.path, tc.body); w.Code != tc.want {
			t.Errorf("%s: got %d, want %d (%s)", name, w.Code, tc.want, w.Body.String())
		}
	}
	if len(h.audit.entries) != 0 {
		t.Fatal("a refused change was audited")
	}
}

type failingSSOVault struct{ ssoSecrets }

func (failingSSOVault) Get(string) (map[string]string, error) { return nil, errors.New("not found") }
func (failingSSOVault) Put(string, map[string]string) error   { return errors.New("sealed") }

func TestSSOProviderReportsAVaultFailure(t *testing.T) {
	h := newSSOHarness(t, failingSSOVault{})
	if w := doRequest(h.router, "PUT", "/api/admin/sso-providers/google", `{"enabled":false,"clientId":"g1"}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", w.Code)
	}
}
