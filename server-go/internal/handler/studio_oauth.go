package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/studiooauth"
)

// oauthStateCookie binds a sign-in to the browser that started it. It is
// SameSite=Lax because the provider returns the browser with a cross-site
// top-level navigation, which a Strict cookie would not survive.
const oauthStateCookie = "excali_oauth_state"

const oauthCookiePath = "/api/auth/oauth"

type studioSignIn interface {
	Configured() []string
	Start(ctx context.Context, provider, inviteHash string) (string, string, error)
	Finish(ctx context.Context, provider, queryState, browserState, code string) (studiooauth.Identity, string, error)
}

type studioIdentities interface {
	FindUserByIdentity(ctx context.Context, provider, subject string) (*domain.User, error)
	FindUserByEmail(ctx context.Context, email string) (*domain.User, error)
	LinkStudioIdentity(ctx context.Context, provider, subject, userID, email string) error
}

// signInRefusal is a sign-in that ends back on Studio's login page with a
// machine-readable reason.
type signInRefusal string

func (r signInRefusal) Error() string { return string(r) }

// StudioOAuthHandler signs developers in to Studio with Google or GitHub.
type StudioOAuthHandler struct {
	signIn     studioSignIn
	auth       *AuthHandler
	identities studioIdentities
	studioURL  string
}

func NewStudioOAuthHandler(signIn studioSignIn, authHandler *AuthHandler, identities studioIdentities, studioURL string) *StudioOAuthHandler {
	return &StudioOAuthHandler{signIn: signIn, auth: authHandler, identities: identities, studioURL: strings.TrimRight(studioURL, "/")}
}

func (h *StudioOAuthHandler) Routes(r chi.Router, limits ...func(http.Handler) http.Handler) {
	r.Get("/providers", h.Providers)
	r.With(limits...).Get("/{provider}/start", h.Start)
	r.With(limits...).Get("/{provider}/callback", h.Callback)
}

// Providers lists the sign-in providers this platform has credentials for;
// Studio shows a button for each and none when the list is empty.
func (h *StudioOAuthHandler) Providers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string][]string{"providers": h.signIn.Configured()})
}

func (h *StudioOAuthHandler) Start(w http.ResponseWriter, r *http.Request) {
	var inviteHash string
	if invite := r.URL.Query().Get("invite"); invite != "" {
		inviteHash = hashToken(invite)
	}
	target, state, err := h.signIn.Start(r.Context(), chi.URLParam(r, "provider"), inviteHash)
	if err != nil {
		log.Printf("WARN: studio sign-in start: %v", err)
		h.refuse(w, r, "unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: oauthStateCookie, Value: state, Path: oauthCookiePath, MaxAge: 600,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, target, http.StatusFound)
}

func (h *StudioOAuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: oauthStateCookie, Value: "", Path: oauthCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
	query := r.URL.Query()
	if query.Get("error") != "" {
		h.refuse(w, r, "cancelled")
		return
	}
	var browserState string
	if cookie, err := r.Cookie(oauthStateCookie); err == nil {
		browserState = cookie.Value
	}
	provider := chi.URLParam(r, "provider")
	identity, inviteHash, err := h.signIn.Finish(r.Context(), provider, query.Get("state"), browserState, query.Get("code"))
	if err != nil {
		h.refuse(w, r, finishRefusal(err))
		return
	}
	user, created, err := h.accountFor(r.Context(), identity, inviteHash)
	if err != nil {
		h.refuse(w, r, accountRefusal(err))
		return
	}
	outcome := url.Values{}
	if created && inviteHash != "" {
		outcome.Set("joined", "1")
	} else if inviteHash != "" {
		h.joinExisting(r.Context(), user, inviteHash, outcome)
	}
	if _, _, err := h.auth.startSession(r.Context(), w, user, "oauth-"+provider); err != nil {
		log.Printf("ERROR: studio sign-in session for %s: %v", user.ID, err)
		h.refuse(w, r, "failed")
		return
	}
	next := h.studioURL + "/oauth/complete"
	if len(outcome) > 0 {
		next += "?" + outcome.Encode()
	}
	http.Redirect(w, r, next, http.StatusFound)
}

// joinExisting spends an invite for an account that already existed, under
// the same rule as accepting while signed in: the invite names the account's
// verified address. A refused invite does not stop the sign-in.
func (h *StudioOAuthHandler) joinExisting(ctx context.Context, user *domain.User, inviteHash string, outcome url.Values) {
	orgs := h.auth.orgStore
	if orgs == nil {
		outcome.Set("invite_error", "invite_invalid")
		return
	}
	invite, err := orgs.FindPendingInviteByToken(ctx, inviteHash, time.Now())
	if err != nil || user.EmailVerifiedAt == nil || !strings.EqualFold(invite.Email, user.Email) {
		outcome.Set("invite_error", "invite_invalid")
		return
	}
	if _, err := orgs.AcceptPendingInvite(ctx, inviteHash, user.ID, time.Now()); err != nil {
		outcome.Set("invite_error", inviteErrorCode(err))
		return
	}
	outcome.Set("joined", "1")
}

func inviteErrorCode(err error) string {
	if errors.Is(err, storage.ErrAlreadyOrgMember) {
		return "already_member"
	}
	return "invite_invalid"
}

func finishRefusal(err error) string {
	switch {
	case errors.Is(err, studiooauth.ErrStateInvalid):
		return "state"
	case errors.Is(err, studiooauth.ErrEmailNotVerified):
		return "email_not_verified"
	default:
		log.Printf("WARN: studio sign-in: %v", err)
		return "failed"
	}
}

func accountRefusal(err error) string {
	var refusal signInRefusal
	if errors.As(err, &refusal) {
		return string(refusal)
	}
	log.Printf("ERROR: studio sign-in account: %v", err)
	return "failed"
}

func (h *StudioOAuthHandler) refuse(w http.ResponseWriter, r *http.Request, reason string) {
	http.Redirect(w, r, h.studioURL+"/login?"+url.Values{"oauth_error": {reason}}.Encode(), http.StatusFound)
}

// accountFor returns the Studio account for a provider identity: the one
// already linked, else the one using the same address (linked now), else a
// new account created under the same rules as password sign-up. created
// reports the last case, whose invite has then already been spent.
func (h *StudioOAuthHandler) accountFor(ctx context.Context, identity studiooauth.Identity, inviteHash string) (*domain.User, bool, error) {
	user, err := h.identities.FindUserByIdentity(ctx, identity.Provider, identity.Subject)
	if err != nil {
		return nil, false, err
	}
	if user == nil {
		user, err = h.identities.FindUserByEmail(ctx, identity.Email)
		if err != nil {
			return nil, false, err
		}
		if user != nil {
			return h.linkExisting(ctx, user, identity)
		}
		return h.createFromIdentity(ctx, identity, inviteHash)
	}
	if user.IsService() || !user.Active {
		return nil, false, signInRefusal("account_conflict")
	}
	return user, false, nil
}

func (h *StudioOAuthHandler) linkExisting(ctx context.Context, user *domain.User, identity studiooauth.Identity) (*domain.User, bool, error) {
	if user.IsService() || !user.Active {
		return nil, false, signInRefusal("account_conflict")
	}
	if user.EmailVerifiedAt == nil {
		if err := h.discardUnprovenCredentials(ctx, user); err != nil {
			return nil, false, err
		}
	}
	if err := h.identities.LinkStudioIdentity(ctx, identity.Provider, identity.Subject, user.ID, identity.Email); err != nil {
		return nil, false, err
	}
	if user.EmailVerifiedAt == nil {
		if err := h.auth.verifier.MarkVerified(ctx, user.ID); err != nil {
			return nil, false, err
		}
		now := time.Now()
		user.EmailVerifiedAt = &now
	}
	return user, false, nil
}

// discardUnprovenCredentials replaces the password and revokes the sessions
// of an account whose address was never proven: whoever registered it may not
// own the address the provider has just proven.
func (h *StudioOAuthHandler) discardUnprovenCredentials(ctx context.Context, user *domain.User) error {
	hash, err := unusablePasswordHash()
	if err != nil {
		return err
	}
	if err := h.auth.userStore.UpdateUserPassword(ctx, user.Username, hash); err != nil {
		return err
	}
	tokens, err := h.auth.tokenStore.ListTokensByUser(ctx, user.ID)
	if err != nil {
		return err
	}
	for _, token := range tokens {
		if err := h.auth.tokenStore.DeleteToken(ctx, token.TokenHash); err != nil {
			return err
		}
	}
	return nil
}

// unusablePasswordHash hashes a random secret nobody holds, so the account
// signs in only through its provider until a reset sets a password.
func unusablePasswordHash() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", err
	}
	return auth.HashPassword(hex.EncodeToString(secret))
}

func (h *StudioOAuthHandler) createFromIdentity(ctx context.Context, identity studiooauth.Identity, inviteHash string) (*domain.User, bool, error) {
	if err := h.checkSignUp(ctx, identity.Email, inviteHash); err != nil {
		return nil, false, err
	}
	user, err := h.newUser(ctx, identity.Email)
	if err != nil {
		return nil, false, err
	}
	if err := h.auth.userStore.CreateUser(ctx, user); err != nil {
		return nil, false, err
	}
	if err := h.identities.LinkStudioIdentity(ctx, identity.Provider, identity.Subject, user.ID, identity.Email); err != nil {
		_ = h.auth.userStore.DeleteUser(ctx, user.ID)
		return nil, false, err
	}
	if inviteHash == "" {
		return user, false, nil
	}
	if status, _ := h.auth.joinInvitedOrg(ctx, inviteHash, user); status != 0 {
		return nil, false, signInRefusal("invite_invalid")
	}
	return user, true, nil
}

// checkSignUp applies password sign-up's rules to a new provider account.
func (h *StudioOAuthHandler) checkSignUp(ctx context.Context, address, inviteHash string) error {
	users, err := h.auth.userStore.FindAllUsers(ctx)
	if err != nil {
		return err
	}
	if len(domain.HumanUsers(users)) == 0 {
		return signInRefusal("setup_required")
	}
	if inviteHash == "" {
		if h.auth.inviteOnly {
			return signInRefusal("invite_only")
		}
		return nil
	}
	if h.auth.orgStore == nil {
		return signInRefusal("invite_invalid")
	}
	invite, err := h.auth.orgStore.FindPendingInviteByToken(ctx, inviteHash, time.Now())
	if err != nil || !strings.EqualFold(invite.Email, address) {
		return signInRefusal("invite_invalid")
	}
	return nil
}

var usernameUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)

// newUser builds a verified account whose username is derived from the
// address. It has no usable password; the owner can set one by reset.
func (h *StudioOAuthHandler) newUser(ctx context.Context, address string) (*domain.User, error) {
	username, err := h.freeUsername(ctx, address)
	if err != nil {
		return nil, err
	}
	hash, err := unusablePasswordHash()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &domain.User{
		ID: auth.GenerateID(), Username: username, Email: address, PasswordHash: hash,
		Role: "user", Active: true, Kind: domain.UserKindHuman, EmailVerifiedAt: &now, CreatedAt: &now,
	}, nil
}

func (h *StudioOAuthHandler) freeUsername(ctx context.Context, address string) (string, error) {
	local, _, _ := strings.Cut(strings.ToLower(address), "@")
	base := strings.Trim(usernameUnsafe.ReplaceAllString(local, "-"), "-")
	if base == "" {
		base = "user"
	}
	candidate := base
	for attempt := 0; attempt < 20; attempt++ {
		existing, err := h.auth.userStore.FindUserByUsername(ctx, candidate)
		if err != nil {
			return "", err
		}
		if existing == nil {
			return candidate, nil
		}
		suffix := make([]byte, 3)
		if _, err := rand.Read(suffix); err != nil {
			return "", err
		}
		candidate = base + "-" + hex.EncodeToString(suffix)
	}
	return "", errors.New("no free username")
}
