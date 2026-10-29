package appregistry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

type tokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

func tokenHandler(t *testing.T, projects map[string]bool) (*TokenHandler, *Issuer) {
	t.Helper()
	issuer, _ := testIssuer(t)
	return NewTokenHandler(issuer, func(_ context.Context, projectID string) (bool, error) {
		if projectID == "proj-broken" {
			return false, errors.New("store down")
		}
		return projects[projectID], nil
	}), issuer
}

func askToken(h http.Handler, method, user, password string, query url.Values) *httptest.ResponseRecorder {
	var req *http.Request
	if method == http.MethodPost {
		form := url.Values{"grant_type": {"password"}, "username": {user}, "password": {password}}
		for k, v := range query {
			form[k] = v
		}
		req = httptest.NewRequest(method, "/internal/registry/token", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, "/internal/registry/token?"+query.Encode(), nil)
		if user != "" {
			req.SetBasicAuth(user, password)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func grantsIn(t *testing.T, issuer *Issuer, rec *httptest.ResponseRecorder) []Grant {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body tokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Token == "" || body.Token != body.AccessToken || body.ExpiresIn != int(pullTokenTTL.Seconds()) {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
	var claims parsedClaims
	if _, err := jwt.ParseWithClaims(body.Token, &claims, func(*jwt.Token) (any, error) { return issuer.key.Public(), nil }); err != nil {
		t.Fatal(err)
	}
	return claims.Access
}

const service = "registry.excalibase.internal"

func TestTokenHandler_GrantsPullOnTheProjectsOwnRepositoriesOnly(t *testing.T) {
	h, issuer := tokenHandler(t, map[string]bool{"proj-a": true})
	cred := issuer.PullCredential("proj-a")
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		rec := askToken(h, method, cred.Username, cred.Password, url.Values{"service": {service}, "scope": {
			"repository:proj-a/api:pull,push", "repository:proj-b/api:pull", "repository:proj-a/web:pull repository:proj-ab/x:pull",
			"registry:catalog:*", "repository:proj-a/nested/x:pull", "garbage"}})
		grants := grantsIn(t, issuer, rec)
		if len(grants) != 2 || grants[0].Name != "proj-a/api" || strings.Join(grants[0].Actions, ",") != "pull" || grants[1].Name != "proj-a/web" {
			t.Errorf("%s grants %+v", method, grants)
		}
	}
}

func TestTokenHandler_RefusesWhoIsNotAProject(t *testing.T) {
	h, issuer := tokenHandler(t, map[string]bool{"proj-a": true})
	scope := url.Values{"service": {service}, "scope": {"repository:proj-a/api:pull"}}
	a := issuer.PullCredential("proj-a")
	cases := map[string]*httptest.ResponseRecorder{
		"anonymous":              askToken(h, http.MethodGet, "", "", scope),
		"a wrong password":       askToken(h, http.MethodGet, "proj-a", "nope", scope),
		"proj-a's password as b": askToken(h, http.MethodGet, "proj-b", a.Password, scope),
		"a deleted project":      askToken(h, http.MethodGet, "proj-gone", issuer.PullCredential("proj-gone").Password, scope),
	}
	for name, rec := range cases {
		if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "token") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := askToken(h, http.MethodGet, "proj-broken", issuer.PullCredential("proj-broken").Password, scope); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("a store that cannot say is not a refusal: %d", rec.Code)
	}
	if rec := askToken(h, http.MethodGet, a.Username, a.Password, url.Values{"service": {"docker.io"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("a token for another service: %d", rec.Code)
	}
	if rec := askToken(h, http.MethodDelete, a.Username, a.Password, scope); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("delete: %d", rec.Code)
	}
	many := url.Values{"service": {service}}
	for range maxScopes + 1 {
		many.Add("scope", "repository:proj-a/api:pull")
	}
	if rec := askToken(h, http.MethodGet, a.Username, a.Password, many); rec.Code != http.StatusBadRequest {
		t.Errorf("too many scopes: %d", rec.Code)
	}
	packed := url.Values{"service": {service}, "scope": {strings.Repeat("repository:proj-a/api:pull ", maxScopes+1)}}
	if rec := askToken(h, http.MethodGet, a.Username, a.Password, packed); rec.Code != http.StatusBadRequest {
		t.Errorf("too many scopes in one field: %d", rec.Code)
	}
}
