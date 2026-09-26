// Package studiooauth signs developers in to Studio with Google or GitHub:
// authorization code with PKCE, a state bound to the browser and spent once,
// and only a provider-verified email address accepted.
package studiooauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// stateTTL bounds how long a sign-in may sit at the provider.
const stateTTL = 10 * time.Minute

var (
	ErrUnknownProvider       = errors.New("unknown sign-in provider")
	ErrProviderNotConfigured = errors.New("sign-in provider is not configured")
	ErrStateInvalid          = errors.New("sign-in state is invalid or has expired")
	ErrEmailNotVerified      = errors.New("the provider has not verified this email address")
)

// Identity is a provider account with a provider-verified email.
type Identity struct {
	Provider string
	Subject  string
	Email    string
}

type secretReader interface {
	Get(path string) (map[string]string, error)
}

// StateStore keeps sign-in state server-side between start and callback.
type StateStore interface {
	SaveOAuthState(ctx context.Context, stateHash string, state domain.OAuthState, expiresAt time.Time) error
	// ConsumeOAuthState returns and deletes a live state; a missing or
	// expired one is ErrStateInvalid.
	ConsumeOAuthState(ctx context.Context, stateHash string, now time.Time) (*domain.OAuthState, error)
}

type Service struct {
	providers    map[string]Provider
	order        []string
	secrets      secretReader
	states       StateStore
	callbackBase string
	http         *http.Client
	now          func() time.Time
}

// New builds the sign-in service. Client ids and secrets are read from the
// vault at oauth/studio/<provider> on each use, never from the browser.
func New(providers []Provider, secrets secretReader, states StateStore, callbackBase string, httpClient *http.Client) *Service {
	s := &Service{
		providers: map[string]Provider{}, secrets: secrets, states: states,
		callbackBase: strings.TrimRight(callbackBase, "/"), http: httpClient, now: time.Now,
	}
	for _, p := range providers {
		s.providers[p.Name] = p
		s.order = append(s.order, p.Name)
	}
	return s
}

// Configured lists the providers that have credentials, in offer order.
func (s *Service) Configured() []string {
	if s == nil {
		return []string{}
	}
	names := []string{}
	for _, name := range s.order {
		if _, err := s.config(name); err == nil {
			names = append(names, name)
		}
	}
	return names
}

// Start records a fresh state and PKCE verifier and returns the provider URL
// to send the browser to, and the state to bind to the browser.
func (s *Service) Start(ctx context.Context, provider, inviteHash string) (string, string, error) {
	cfg, err := s.config(provider)
	if err != nil {
		return "", "", err
	}
	state, err := randomToken()
	if err != nil {
		return "", "", err
	}
	verifier := oauth2.GenerateVerifier()
	record := domain.OAuthState{Provider: provider, CodeVerifier: verifier, InviteHash: inviteHash}
	if err := s.states.SaveOAuthState(ctx, hashState(state), record, s.now().Add(stateTTL)); err != nil {
		return "", "", fmt.Errorf("save sign-in state: %w", err)
	}
	return cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), state, nil
}

// Finish checks the callback's state against the browser's, exchanges the
// code with the recorded verifier and returns the provider-verified identity
// plus the invite the sign-in started from.
func (s *Service) Finish(ctx context.Context, provider, queryState, browserState, code string) (Identity, string, error) {
	if queryState == "" || subtle.ConstantTimeCompare([]byte(queryState), []byte(browserState)) != 1 {
		return Identity{}, "", ErrStateInvalid
	}
	record, err := s.states.ConsumeOAuthState(ctx, hashState(queryState), s.now())
	if err != nil {
		return Identity{}, "", ErrStateInvalid
	}
	if record.Provider != provider {
		return Identity{}, "", ErrStateInvalid
	}
	cfg, err := s.config(provider)
	if err != nil {
		return Identity{}, "", err
	}
	token, err := cfg.Exchange(context.WithValue(ctx, oauth2.HTTPClient, s.http), code, oauth2.VerifierOption(record.CodeVerifier))
	if err != nil {
		return Identity{}, "", fmt.Errorf("exchange the authorization code: %w", err)
	}
	p := s.providers[provider]
	identity, err := p.identify(ctx, s.http, p.Endpoints.APIBase, token.AccessToken)
	if err != nil {
		return Identity{}, "", err
	}
	return identity, record.InviteHash, nil
}

func (s *Service) config(provider string) (*oauth2.Config, error) {
	p, ok := s.providers[provider]
	if !ok {
		return nil, ErrUnknownProvider
	}
	if s.secrets == nil {
		return nil, ErrProviderNotConfigured
	}
	creds, err := s.secrets.Get("oauth/studio/" + provider)
	if err != nil || creds["client_id"] == "" || creds["client_secret"] == "" {
		return nil, ErrProviderNotConfigured
	}
	return &oauth2.Config{
		ClientID:     creds["client_id"],
		ClientSecret: creds["client_secret"],
		Endpoint:     oauth2.Endpoint{AuthURL: p.Endpoints.AuthURL, TokenURL: p.Endpoints.TokenURL},
		RedirectURL:  s.callbackBase + "/api/auth/oauth/" + provider + "/callback",
		Scopes:       p.Scopes,
	}, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("sign-in state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func hashState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}
