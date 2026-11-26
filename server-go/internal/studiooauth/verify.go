package studiooauth

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

const (
	// PurposeVerify marks a state that started a GitHub verification, not a sign-in.
	PurposeVerify = "verify"
	// verifyStatePrefix lets the one registered callback tell a verification from a sign-in before it spends the state.
	verifyStatePrefix = "v."
	githubProvider    = "github"
	// eventsPageSize is one page of public events; enough to count the few the rule asks for.
	eventsPageSize = 100
)

// IsVerifyState reports whether a callback's state began a verification.
func IsVerifyState(state string) bool { return strings.HasPrefix(state, verifyStatePrefix) }

// StartVerify sends a signed-in account to GitHub to prove its GitHub account.
// It returns the URL to send the browser to and the state to bind to it.
func (s *Service) StartVerify(ctx context.Context, userID string) (string, string, error) {
	cfg, err := s.config(githubProvider)
	if err != nil {
		return "", "", err
	}
	token, err := randomToken()
	if err != nil {
		return "", "", err
	}
	state := verifyStatePrefix + token
	verifier := oauth2.GenerateVerifier()
	record := domain.OAuthState{Provider: githubProvider, CodeVerifier: verifier, Purpose: PurposeVerify, UserID: userID}
	if err := s.states.SaveOAuthState(ctx, hashState(state), record, s.now().Add(stateTTL)); err != nil {
		return "", "", fmt.Errorf("save verification state: %w", err)
	}
	return cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), state, nil
}

// Verification is a finished GitHub round trip: the account it started from
// and the user's token for the evidence calls.
type Verification struct {
	UserID      string
	AccessToken string
}

// FinishVerify checks the callback's state against the browser's, spends it
// and exchanges the code. It refuses a state that began a sign-in.
func (s *Service) FinishVerify(ctx context.Context, queryState, browserState, code string) (Verification, error) {
	if !IsVerifyState(queryState) || subtle.ConstantTimeCompare([]byte(queryState), []byte(browserState)) != 1 {
		return Verification{}, ErrStateInvalid
	}
	record, err := s.states.ConsumeOAuthState(ctx, hashState(queryState), s.now())
	if err != nil || record.Provider != githubProvider || record.Purpose != PurposeVerify || record.UserID == "" {
		return Verification{}, ErrStateInvalid
	}
	cfg, err := s.config(githubProvider)
	if err != nil {
		return Verification{}, err
	}
	token, err := cfg.Exchange(context.WithValue(ctx, oauth2.HTTPClient, s.http), code, oauth2.VerifierOption(record.CodeVerifier))
	if err != nil {
		return Verification{}, fmt.Errorf("exchange the authorization code: %w", err)
	}
	return Verification{UserID: record.UserID, AccessToken: token.AccessToken}, nil
}

type githubUser struct {
	ID        int64     `json:"id"`
	Login     string    `json:"login"`
	CreatedAt time.Time `json:"created_at"`
}

type githubDated struct {
	CreatedAt time.Time `json:"created_at"`
	Fork      bool      `json:"fork"`
	Archived  bool      `json:"archived"`
}

// Evidence reads the account's age, its public events inside the activity
// window and its own public repositories, with the user's token. Forks and
// archived repositories do not count: they cost nothing to make.
func (s *Service) Evidence(ctx context.Context, accessToken string) (trust.Evidence, error) {
	base := s.providers[githubProvider].Endpoints.APIBase
	var user githubUser
	if err := getJSON(ctx, s.http, base+"/user", accessToken, &user); err != nil {
		return trust.Evidence{}, err
	}
	if user.ID == 0 || user.Login == "" {
		return trust.Evidence{}, fmt.Errorf("github did not name the account")
	}
	login := url.PathEscape(user.Login)
	var events, repos []githubDated
	if err := getJSON(ctx, s.http, base+"/users/"+login+"/events/public?per_page="+strconv.Itoa(eventsPageSize), accessToken, &events); err != nil {
		return trust.Evidence{}, err
	}
	if err := getJSON(ctx, s.http, base+"/users/"+login+"/repos?type=owner&per_page=100", accessToken, &repos); err != nil {
		return trust.Evidence{}, err
	}
	now := s.now()
	evidence := trust.Evidence{GitHubID: strconv.FormatInt(user.ID, 10), AccountAgeDays: wholeDays(now, user.CreatedAt)}
	window := now.AddDate(0, 0, -s.ActivityWindowDays)
	for _, event := range events {
		if event.CreatedAt.After(window) {
			evidence.PublicEvents++
		}
	}
	for _, repo := range repos {
		if repo.Fork || repo.Archived {
			continue
		}
		evidence.PublicRepos++
		evidence.OldestRepoAgeDays = max(evidence.OldestRepoAgeDays, wholeDays(now, repo.CreatedAt))
	}
	return evidence, nil
}

func wholeDays(now, then time.Time) int {
	return max(0, int(now.Sub(then).Hours()/24))
}

var _ trust.ProfileSource = (*Service)(nil)
