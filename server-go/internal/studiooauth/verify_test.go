package studiooauth

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestVerifyFlowCarriesTheAccountAndTheToken(t *testing.T) {
	fake := newFakeProvider(t)
	svc, states := newService(t, fake, configured())
	redirect, state, err := svc.StartVerify(context.Background(), "user-7")
	if err != nil || !IsVerifyState(state) || !strings.Contains(redirect, "state="+state) {
		t.Fatalf("StartVerify: %q %q %v", redirect, state, err)
	}
	record := states.records[hashState(state)].state
	if record.Purpose != PurposeVerify || record.UserID != "user-7" || record.Provider != "github" {
		t.Fatalf("recorded %+v", record)
	}
	fake.challenge = challengeOf(t, redirect)
	got, err := svc.FinishVerify(context.Background(), state, state, "the-code")
	if err != nil || got.UserID != "user-7" || got.AccessToken != "provider-access-token" {
		t.Fatalf("FinishVerify: %+v %v", got, err)
	}
	if _, err := svc.FinishVerify(context.Background(), state, state, "the-code"); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("the state was usable twice: %v", err)
	}
}

// A sign-in state cannot finish a verification, and a verification state cannot sign anyone in.
func TestVerifyAndSignInStatesAreNotInterchangeable(t *testing.T) {
	fake := newFakeProvider(t)
	svc, _ := newService(t, fake, configured())
	signIn := start(t, svc, fake, "github", "")
	if _, err := svc.FinishVerify(context.Background(), signIn, signIn, "the-code"); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("a sign-in state finished a verification: %v", err)
	}
	_, verifyState, _ := svc.StartVerify(context.Background(), "user-7")
	if _, _, err := svc.Finish(context.Background(), "github", verifyState, verifyState, "the-code"); !errors.Is(err, ErrStateInvalid) {
		t.Errorf("a verification state signed someone in: %v", err)
	}
}

func challengeOf(t *testing.T, redirect string) string {
	t.Helper()
	u, err := url.Parse(redirect)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query().Get("code_challenge")
}

func TestVerifyNeedsGitHubConfigured(t *testing.T) {
	fake := newFakeProvider(t)
	svc, _ := newService(t, fake, mapSecrets{})
	if _, _, err := svc.StartVerify(context.Background(), "user-7"); !errors.Is(err, ErrProviderNotConfigured) {
		t.Fatalf("got %v", err)
	}
}

type githubAPI struct {
	user   map[string]any
	events []map[string]any
	repos  []map[string]any
	token  string
}

func (g *githubAPI) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	guard := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+g.token {
				t.Errorf("%s without the user's token", r.URL.Path)
			}
			next(w, r)
		}
	}
	mux.HandleFunc("/user", guard(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(g.user) }))
	mux.HandleFunc("/users/octo/events/public", guard(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(g.events) }))
	mux.HandleFunc("/users/octo/repos", guard(func(w http.ResponseWriter, _ *http.Request) { _ = json.NewEncoder(w).Encode(g.repos) }))
	return mux
}

var evidenceNow = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

func daysAgo(days int) string { return evidenceNow.AddDate(0, 0, -days).Format(time.RFC3339) }

type evidenceResult struct {
	id                         string
	age, events, repos, oldest int
}

func fetchEvidence(t *testing.T, api *githubAPI) (evidenceResult, error) {
	t.Helper()
	server := httptest.NewServer(api.handler(t))
	t.Cleanup(server.Close)
	svc := New([]Provider{GitHub(Endpoints{APIBase: server.URL})}, nil, nil, callbackBase, server.Client())
	svc.now = func() time.Time { return evidenceNow }
	svc.ActivityWindowDays = 90
	got, err := svc.Evidence(context.Background(), api.token)
	return evidenceResult{got.GitHubID, got.AccountAgeDays, got.PublicEvents, got.PublicRepos, got.OldestRepoAgeDays}, err
}

func TestGitHubEvidenceCountsAgeEventsInTheWindowAndOwnRepos(t *testing.T) {
	api := &githubAPI{
		token: "tok",
		user:  map[string]any{"id": 4242, "login": "octo", "created_at": daysAgo(200)},
		events: []map[string]any{
			{"created_at": daysAgo(1)}, {"created_at": daysAgo(30)}, {"created_at": daysAgo(89)},
			{"created_at": daysAgo(91)}, {"created_at": daysAgo(400)},
		},
		repos: []map[string]any{
			{"created_at": daysAgo(10), "fork": false}, {"created_at": daysAgo(150), "fork": false},
			{"created_at": daysAgo(300), "fork": true}, {"created_at": daysAgo(20), "fork": false, "archived": true},
		},
	}
	got, err := fetchEvidence(t, api)
	if err != nil {
		t.Fatal(err)
	}
	want := evidenceResult{id: "4242", age: 200, events: 3, repos: 2, oldest: 150}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestGitHubEvidenceOfANewAccountWithNothing(t *testing.T) {
	got, err := fetchEvidence(t, &githubAPI{token: "tok", user: map[string]any{"id": 9, "login": "octo", "created_at": daysAgo(2)}})
	if err != nil || got != (evidenceResult{id: "9", age: 2}) {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestGitHubEvidenceFailsWhenGitHubDoes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }))
	defer server.Close()
	svc := New([]Provider{GitHub(Endpoints{APIBase: server.URL})}, nil, nil, callbackBase, server.Client())
	if _, err := svc.Evidence(context.Background(), "tok"); err == nil {
		t.Fatal("a refused API call counted as evidence")
	}
}

// A state that carries the verify prefix but was not saved as a verification, or names no account, finishes nothing.
func TestFinishVerifyRefusesAMalformedRecord(t *testing.T) {
	cases := map[string]domain.OAuthState{
		"a sign-in record under a verify-looking state": {Provider: "github", CodeVerifier: "cv", Purpose: "", UserID: "user-7"},
		"another purpose":  {Provider: "github", CodeVerifier: "cv", Purpose: "invite", UserID: "user-7"},
		"no account":       {Provider: "github", CodeVerifier: "cv", Purpose: PurposeVerify, UserID: ""},
		"another provider": {Provider: "google", CodeVerifier: "cv", Purpose: PurposeVerify, UserID: "user-7"},
	}
	for name, record := range cases {
		fake := newFakeProvider(t)
		svc, states := newService(t, fake, configured())
		state := verifyStatePrefix + "forged"
		states.records[hashState(state)] = memState{state: record, expires: time.Now().Add(time.Minute)}
		if _, err := svc.FinishVerify(context.Background(), state, state, "the-code"); !errors.Is(err, ErrStateInvalid) {
			t.Errorf("%s: err = %v, want ErrStateInvalid", name, err)
		}
	}
}
