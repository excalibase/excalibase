package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/studiooauth"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

type trustVerifier interface {
	Begin(ctx context.Context, userID string, emailVerified bool) error
	Complete(ctx context.Context, userID, accessToken string) (trust.Attempt, error)
}

type githubVerifyFlow interface {
	Configured() []string
	StartVerify(ctx context.Context, userID string) (string, string, error)
	FinishVerify(ctx context.Context, queryState, browserState, code string) (studiooauth.Verification, error)
}

type trustReader interface {
	Current(ctx context.Context, userID string) (*trust.Record, error)
	Latest(ctx context.Context, userID string) (*trust.Attempt, error)
}

// TrustVerifyHandler is how a limited account becomes full: it proves its
// GitHub account (EXC-899). Studio reads GET /api/trust, starts the round trip
// with POST /api/trust/github/start and is sent back to /?verify=<result>.
type TrustVerifyHandler struct {
	verifier  trustVerifier
	github    githubVerifyFlow
	reader    trustReader
	policy    trust.Policy
	wait      time.Duration
	studioURL string
}

func NewTrustVerifyHandler(verifier trustVerifier, github githubVerifyFlow, reader trustReader, policy trust.Policy, wait time.Duration, studioURL string) *TrustVerifyHandler {
	return &TrustVerifyHandler{verifier: verifier, github: github, reader: reader, policy: policy, wait: wait, studioURL: strings.TrimRight(studioURL, "/")}
}

// Routes mounts under /api/trust behind RequireAuth.
func (h *TrustVerifyHandler) Routes(r chi.Router) {
	r.Get("/", h.Status)
	r.Post("/github/start", h.Start)
}

type trustPolicyView struct {
	MinAgeDays         int `json:"minAgeDays"`
	MinPublicEvents    int `json:"minPublicEvents"`
	ActivityWindowDays int `json:"activityWindowDays"`
	MinRepos           int `json:"minRepos"`
	RepoMinAgeDays     int `json:"repoMinAgeDays"`
}

type trustEvidenceView struct {
	AccountAgeDays    int `json:"accountAgeDays"`
	PublicEvents      int `json:"publicEvents"`
	PublicRepos       int `json:"publicRepos"`
	OldestRepoAgeDays int `json:"oldestRepoAgeDays"`
}

type trustAttemptView struct {
	CheckedAt time.Time         `json:"checkedAt"`
	Passed    bool              `json:"passed"`
	Reason    trust.Reason      `json:"reason"`
	Missing   []string          `json:"missing"`
	Evidence  trustEvidenceView `json:"evidence"`
}

type trustStatusView struct {
	State             trust.State       `json:"state"`
	Method            trust.Method      `json:"method,omitempty"`
	Reason            trust.Reason      `json:"reason,omitempty"`
	EmailVerified     bool              `json:"emailVerified"`
	GitHubConfigured  bool              `json:"githubConfigured"`
	Policy            trustPolicyView   `json:"policy"`
	RetryAfterSeconds int               `json:"retryAfterSeconds,omitempty"`
	LastAttempt       *trustAttemptView `json:"lastAttempt,omitempty"`
}

// Status is what the "Verify your account" card shows. The GitHub account's
// id stays on the server; only counts and ages go out.
func (h *TrustVerifyHandler) Status(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	current, err := h.reader.Current(r.Context(), user.ID)
	if err != nil {
		log.Printf("ERROR: trust status: %v", err)
		httpError(w, "the verification status could not be read", http.StatusInternalServerError)
		return
	}
	last, err := h.reader.Latest(r.Context(), user.ID)
	if err != nil {
		log.Printf("ERROR: trust status: %v", err)
		httpError(w, "the verification status could not be read", http.StatusInternalServerError)
		return
	}
	view := trustStatusView{
		State: trust.Limited, EmailVerified: user.EmailVerifiedAt != nil, GitHubConfigured: len(h.github.Configured()) > 0,
		Policy: trustPolicyView{h.policy.MinAgeDays, h.policy.MinPublicEvents, h.policy.ActivityWindowDays, h.policy.MinRepos, h.policy.RepoMinAgeDays},
	}
	if current != nil {
		view.State, view.Method, view.Reason = current.State, current.Method, current.Reason
	}
	if last != nil {
		view.LastAttempt = &trustAttemptView{
			CheckedAt: last.CheckedAt, Passed: last.Passed, Reason: last.Reason,
			Missing:  nonNil(h.policy.Explain(last.Passed, last.Reason, last.Evidence)),
			Evidence: trustEvidenceView{last.Evidence.AccountAgeDays, last.Evidence.PublicEvents, last.Evidence.PublicRepos, last.Evidence.OldestRepoAgeDays},
		}
		if left := time.Until(last.CheckedAt.Add(h.wait)); left > 0 && !last.Passed {
			view.RetryAfterSeconds = int(left.Seconds()) + 1
		}
	}
	writeJSON(w, view)
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// Start answers the GitHub URL to send the browser to and binds the round
// trip to this browser with the state cookie.
func (h *TrustVerifyHandler) Start(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if err := h.verifier.Begin(r.Context(), user.ID, user.EmailVerifiedAt != nil); err != nil {
		writeVerifyRefusal(w, err)
		return
	}
	target, state, err := h.github.StartVerify(r.Context(), user.ID)
	if err != nil {
		log.Printf("WARN: trust verify start: %v", err)
		httpError(w, "Verifying needs GitHub sign-in, which is not set up on this platform.", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, auth.NewCookie(oauthStateCookie, state, 600, http.SameSiteLaxMode))
	writeJSON(w, map[string]string{"url": target})
}

func writeVerifyRefusal(w http.ResponseWriter, err error) {
	var soon *trust.TooSoonError
	switch {
	case errors.Is(err, trust.ErrVerifyOff):
		httpError(w, errNotFound, http.StatusNotFound)
	case errors.As(err, &soon):
		w.Header().Set(headerRetryAfter, strconv.Itoa(int(soon.RetryAfter.Seconds())))
		httpError(w, err.Error(), http.StatusTooManyRequests)
	case errors.Is(err, trust.ErrEmailNotVerified), errors.Is(err, trust.ErrAlreadyVerified):
		httpError(w, err.Error(), http.StatusConflict)
	case errors.Is(err, trust.ErrAdminHold):
		httpError(w, err.Error(), http.StatusForbidden)
	default:
		log.Printf("ERROR: trust verify: %v", err)
		httpError(w, "verification could not be started; try again", http.StatusInternalServerError)
	}
}

// Callback finishes a round trip the shared GitHub callback hands over.
func (h *TrustVerifyHandler) Callback(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, auth.NewCookie(oauthStateCookie, "", -1, http.SameSiteLaxMode))
	query := r.URL.Query()
	if query.Get("error") != "" {
		h.back(w, r, "cancelled")
		return
	}
	browserState, _ := auth.ReadCookie(r, oauthStateCookie)
	finished, err := h.github.FinishVerify(r.Context(), query.Get("state"), browserState, query.Get("code"))
	if err != nil {
		log.Printf("WARN: trust verify finish: %v", err)
		h.back(w, r, "state")
		return
	}
	attempt, err := h.verifier.Complete(r.Context(), finished.UserID, finished.AccessToken)
	h.back(w, r, callbackOutcome(attempt, err))
}

func callbackOutcome(attempt trust.Attempt, err error) string {
	var soon *trust.TooSoonError
	switch {
	case errors.Is(err, trust.ErrVerifyOff):
		return "off"
	case errors.As(err, &soon):
		return "too_soon"
	case errors.Is(err, trust.ErrAlreadyVerified):
		return "passed"
	case err != nil:
		log.Printf("ERROR: trust verify complete: %v", err)
		return "error"
	case attempt.Passed:
		return "passed"
	}
	return "failed"
}

func (h *TrustVerifyHandler) back(w http.ResponseWriter, r *http.Request, outcome string) {
	http.Redirect(w, r, h.studioURL+"/?"+url.Values{"verify": {outcome}}.Encode(), http.StatusFound)
}
