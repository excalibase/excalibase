package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/studiooauth"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

type fakeVerify struct {
	beginErr    error
	attempt     trust.Attempt
	completeErr error
	begun       []string
	completed   []string
}

func (f *fakeVerify) Begin(_ context.Context, userID string, emailVerified bool) error {
	f.begun = append(f.begun, userID)
	if !emailVerified {
		return trust.ErrEmailNotVerified
	}
	return f.beginErr
}

func (f *fakeVerify) Complete(_ context.Context, userID, token string) (trust.Attempt, error) {
	f.completed = append(f.completed, userID+":"+token)
	return f.attempt, f.completeErr
}

type fakeGitHubFlow struct {
	startErr  error
	finishErr error
	user      string
}

func (f *fakeGitHubFlow) Configured() []string { return []string{"github"} }

func (f *fakeGitHubFlow) StartVerify(_ context.Context, userID string) (string, string, error) {
	return "https://github.example/authorize?state=v.abc", "v.abc", f.startErr
}

func (f *fakeGitHubFlow) FinishVerify(_ context.Context, queryState, browserState, _ string) (studiooauth.Verification, error) {
	if queryState != browserState || f.finishErr != nil {
		return studiooauth.Verification{}, errors.Join(studiooauth.ErrStateInvalid, f.finishErr)
	}
	return studiooauth.Verification{UserID: f.user, AccessToken: "gh-token"}, nil
}

type fakeTrustReader struct {
	current *trust.Record
	last    *trust.Attempt
}

func (f fakeTrustReader) Current(context.Context, string) (*trust.Record, error) {
	return f.current, nil
}

func (f fakeTrustReader) Latest(context.Context, string) (*trust.Attempt, error) { return f.last, nil }

const verifyStudio = "https://studio.example"

func verifyHandler(v *fakeVerify, flow *fakeGitHubFlow, reader fakeTrustReader) (*TrustVerifyHandler, http.Handler) {
	policy := trust.Policy{MinAgeDays: 90, MinPublicEvents: 5, ActivityWindowDays: 90, MinRepos: 3, RepoMinAgeDays: 30}
	h := NewTrustVerifyHandler(v, flow, reader, policy, 10*time.Minute, verifyStudio)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			verified := time.Now()
			user := &domain.User{ID: "u1", Active: true, EmailVerifiedAt: &verified}
			if req.Header.Get("X-No-Email") != "" {
				user.EmailVerifiedAt = nil
			}
			next.ServeHTTP(w, req.WithContext(auth.SetUser(req.Context(), user)))
		})
	})
	r.Route("/api/trust", h.Routes)
	return h, r
}

func call(h http.Handler, method, path string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestTrustStatusForALimitedAccountSaysWhatItNeeds(t *testing.T) {
	last := &trust.Attempt{CheckedAt: time.Now().Add(-time.Hour), Reason: trust.ReasonGitHubTooNew, Evidence: trust.Evidence{GitHubID: "1", AccountAgeDays: 12, PublicEvents: 9}}
	_, router := verifyHandler(&fakeVerify{}, &fakeGitHubFlow{}, fakeTrustReader{current: &trust.Record{State: trust.Limited, Reason: trust.ReasonGitHubTooNew}, last: last})
	w := call(router, http.MethodGet, "/api/trust/")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var body struct {
		State         string `json:"state"`
		GitHub        bool   `json:"githubConfigured"`
		EmailVerified bool   `json:"emailVerified"`
		Policy        struct {
			MinAgeDays int `json:"minAgeDays"`
		} `json:"policy"`
		LastAttempt struct {
			Passed   bool     `json:"passed"`
			Missing  []string `json:"missing"`
			Evidence struct {
				AccountAgeDays int `json:"accountAgeDays"`
			} `json:"evidence"`
		} `json:"lastAttempt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.State != "limited" || !body.GitHub || !body.EmailVerified || body.Policy.MinAgeDays != 90 ||
		body.LastAttempt.Passed || len(body.LastAttempt.Missing) == 0 || body.LastAttempt.Evidence.AccountAgeDays != 12 {
		t.Errorf("status %s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"1"`) {
		t.Errorf("the GitHub id was sent to the browser: %s", w.Body.String())
	}
}

func TestTrustStatusWithNoDecisionIsLimitedAndFullIsFull(t *testing.T) {
	_, none := verifyHandler(&fakeVerify{}, &fakeGitHubFlow{}, fakeTrustReader{})
	if w := call(none, http.MethodGet, "/api/trust/"); !strings.Contains(w.Body.String(), `"state":"limited"`) || strings.Contains(w.Body.String(), "lastAttempt") {
		t.Errorf("no decision: %s", w.Body.String())
	}
	_, full := verifyHandler(&fakeVerify{}, &fakeGitHubFlow{}, fakeTrustReader{current: &trust.Record{State: trust.Full}})
	if w := call(full, http.MethodGet, "/api/trust/"); !strings.Contains(w.Body.String(), `"state":"full"`) {
		t.Errorf("full: %s", w.Body.String())
	}
}

func TestStartSendsTheAccountToGitHubWithABoundState(t *testing.T) {
	v := &fakeVerify{}
	_, router := verifyHandler(v, &fakeGitHubFlow{}, fakeTrustReader{})
	w := call(router, http.MethodPost, "/api/trust/github/start")
	var body struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != http.StatusOK || body.URL == "" || len(v.begun) != 1 || v.begun[0] != "u1" {
		t.Fatalf("%d %s begun=%v", w.Code, w.Body.String(), v.begun)
	}
	if cookie := w.Header().Get("Set-Cookie"); !strings.Contains(cookie, auth.CookieName(oauthStateCookie)+"=v.abc") {
		t.Errorf("the state is not bound to the browser: %q", cookie)
	}
}

func TestStartRefusalsExplainThemselves(t *testing.T) {
	cases := []struct {
		name   string
		v      *fakeVerify
		flow   *fakeGitHubFlow
		header []string
		code   int
		says   string
	}{
		{"email", &fakeVerify{}, &fakeGitHubFlow{}, []string{"X-No-Email", "1"}, http.StatusConflict, "email"},
		{"already", &fakeVerify{beginErr: trust.ErrAlreadyVerified}, &fakeGitHubFlow{}, nil, http.StatusConflict, "already verified"},
		{"held", &fakeVerify{beginErr: trust.ErrAdminHold}, &fakeGitHubFlow{}, nil, http.StatusForbidden, "contact support"},
		{"too soon", &fakeVerify{beginErr: &trust.TooSoonError{RetryAfter: 7 * time.Minute}}, &fakeGitHubFlow{}, nil, http.StatusTooManyRequests, "7 minutes"},
		{"off", &fakeVerify{beginErr: trust.ErrVerifyOff}, &fakeGitHubFlow{}, nil, http.StatusNotFound, ""},
		{"github not set up", &fakeVerify{}, &fakeGitHubFlow{startErr: studiooauth.ErrProviderNotConfigured}, nil, http.StatusServiceUnavailable, "GitHub sign-in"},
	}
	for _, tc := range cases {
		_, router := verifyHandler(tc.v, tc.flow, fakeTrustReader{})
		w := call(router, http.MethodPost, "/api/trust/github/start", tc.header...)
		if w.Code != tc.code || !strings.Contains(w.Body.String(), tc.says) {
			t.Errorf("%s: %d %s", tc.name, w.Code, w.Body.String())
		}
		if tc.code == http.StatusTooManyRequests && w.Header().Get("Retry-After") != "420" {
			t.Errorf("%s: Retry-After %q", tc.name, w.Header().Get("Retry-After"))
		}
	}
}

func callbackTo(h *TrustVerifyHandler, state, browser string, extra url.Values) *httptest.ResponseRecorder {
	query := url.Values{"state": {state}, "code": {"c"}}
	for k, v := range extra {
		query[k] = v
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/oauth/github/callback?"+query.Encode(), nil)
	if browser != "" {
		req.AddCookie(&http.Cookie{Name: auth.CookieName(oauthStateCookie), Value: browser})
	}
	w := httptest.NewRecorder()
	h.Callback(w, req)
	return w
}

func location(w *httptest.ResponseRecorder) string { return w.Header().Get("Location") }

func TestCallbackRecordsAndReturnsToStudio(t *testing.T) {
	v := &fakeVerify{attempt: trust.Attempt{Passed: true, Reason: trust.ReasonOK}}
	h, _ := verifyHandler(v, &fakeGitHubFlow{user: "u1"}, fakeTrustReader{})
	w := callbackTo(h, "v.abc", "v.abc", nil)
	if w.Code != http.StatusFound || location(w) != verifyStudio+"/?verify=passed" || len(v.completed) != 1 || v.completed[0] != "u1:gh-token" {
		t.Fatalf("%d %s %v", w.Code, location(w), v.completed)
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Errorf("the state cookie was not cleared: %q", w.Header().Get("Set-Cookie"))
	}
}

func TestCallbackOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		v       *fakeVerify
		flow    *fakeGitHubFlow
		browser string
		extra   url.Values
		want    string
	}{
		{"rule failed", &fakeVerify{attempt: trust.Attempt{Reason: trust.ReasonGitHubTooNew}}, &fakeGitHubFlow{user: "u1"}, "v.abc", nil, "failed"},
		{"cancelled at GitHub", &fakeVerify{}, &fakeGitHubFlow{user: "u1"}, "v.abc", url.Values{"error": {"access_denied"}}, "cancelled"},
		{"state does not match the browser", &fakeVerify{}, &fakeGitHubFlow{user: "u1"}, "other", nil, "state"},
		{"too soon", &fakeVerify{completeErr: &trust.TooSoonError{RetryAfter: time.Minute}}, &fakeGitHubFlow{user: "u1"}, "v.abc", nil, "too_soon"},
		{"flag turned off meanwhile", &fakeVerify{completeErr: trust.ErrVerifyOff}, &fakeGitHubFlow{user: "u1"}, "v.abc", nil, "off"},
		{"store error", &fakeVerify{completeErr: errors.New("db down")}, &fakeGitHubFlow{user: "u1"}, "v.abc", nil, "error"},
	}
	for _, tc := range cases {
		h, _ := verifyHandler(tc.v, tc.flow, fakeTrustReader{})
		w := callbackTo(h, "v.abc", tc.browser, tc.extra)
		if want := verifyStudio + "/?verify=" + tc.want; location(w) != want {
			t.Errorf("%s: redirected to %q, want %q", tc.name, location(w), want)
		}
		if strings.Contains(location(w), "db down") {
			t.Errorf("%s: the cause leaked", tc.name)
		}
	}
}

// GitHub has one registered callback: a verification's state sends it to the verify handler, a sign-in's does not.
func TestTheSharedGitHubCallbackHandsVerificationsOver(t *testing.T) {
	verified := false
	oauth := NewStudioOAuthHandler(&fakeSignIn{}, nil, nil, verifyStudio)
	oauth.SetVerifyCallback(func(http.ResponseWriter, *http.Request) { verified = true })
	r := chi.NewRouter()
	r.Route("/oauth", func(r chi.Router) { oauth.Routes(r) })

	req := httptest.NewRequest(http.MethodGet, "/oauth/github/callback?state=v.abc&code=c", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)
	if !verified {
		t.Fatal("a verification state did not reach the verify handler")
	}
	verified = false
	req = httptest.NewRequest(http.MethodGet, "/oauth/github/callback?error=denied&state=plain", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)
	if verified {
		t.Error("a sign-in state reached the verify handler")
	}
	req = httptest.NewRequest(http.MethodGet, "/oauth/google/callback?error=denied&state=v.abc", nil)
	r.ServeHTTP(httptest.NewRecorder(), req)
	if verified {
		t.Error("a Google callback reached the verify handler")
	}
}
