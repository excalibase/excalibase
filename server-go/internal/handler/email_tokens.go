package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

// EmailTokensHandler covers the three flows that need a click-link token:
//
//   - email verification (POST /verify/send → POST /verify/confirm)
//   - password reset      (POST /reset/send  → POST /reset/confirm)
//   - org invite          (POST /api/orgs/{id}/invite → POST /api/orgs/invite/accept)
//
// All tokens are 32 random bytes, stored hashed (SHA-256) in platform-db
// so a database leak doesn't expose live click links. Each row has a
// consumed_at column so a token can only be used once.
//
// The two public sends mail whoever an address names, so each is limited per
// client and per address, and answers before it looks the address up.
type EmailTokensHandler struct {
	db          *sql.DB         // platform-db, holds password_resets
	sender      email.Sender    // SES, Resend, or noop in dev
	store       emailTokenUsers // user lookup for verify/reset
	studioURL   string          // Studio origin the click links open
	productName string
	pg          bool // true if backing db is Postgres (uses $1 placeholders)
	verifier    *EmailVerifier
	sessions    sessionTokens // ended on a password reset
	publicLimit func(http.Handler) http.Handler
	// confirmLimit bounds attempts at the emailed links per client.
	confirmLimit func(http.Handler) http.Handler
	// addressLimit caps mail to one address across every client.
	addressLimit *middleware.KeyLimiter
	// runInBackground runs a send after the answer is written.
	runInBackground func(func())
}

// Public mail budgets: five sends per client and three per address an hour;
// thirty link attempts per client a minute.
const (
	publicSendsPerClient   = 5
	publicSendsPerAddress  = 3
	publicSendWindow       = time.Hour
	confirmsPerClient      = 30
	confirmWindow          = time.Minute
	backgroundMailDeadline = 30 * time.Second
	passwordResetLifetime  = time.Hour
)

// emailTokenUsers is the slice of the user store the click-link flows need.
type emailTokenUsers interface {
	FindUserByEmail(ctx context.Context, email string) (*domain.User, error)
	FindUserByID(ctx context.Context, id string) (*domain.User, error)
	UpdateUserPassword(ctx context.Context, username, passwordHash string) error
}

// sessionTokens is the slice of the token store a reset needs to end every
// session and revoke every access token the account holds.
type sessionTokens interface {
	ListTokensByUser(ctx context.Context, userID string) ([]*domain.AccessToken, error)
	DeleteToken(ctx context.Context, tokenHash string) error
}

// SetSessionStore wires where a reset ends the account's sessions and access
// tokens. A reset is refused without it.
func (h *EmailTokensHandler) SetSessionStore(s sessionTokens) { h.sessions = s }

var (
	errSessionsNotEnded     = errors.New("sessions not ended")
	errAccessTokensNotEnded = errors.New("access tokens not revoked")
)

// revokeCredentials ends every session, then revokes every access token, and
// returns how many access tokens it revoked.
func (h *EmailTokensHandler) revokeCredentials(ctx context.Context, userID string) (int, error) {
	tokens, err := h.sessions.ListTokensByUser(ctx, userID)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", errSessionsNotEnded, err)
	}
	var accessTokens []*domain.AccessToken
	for _, t := range tokens {
		if !isSessionToken(t) {
			accessTokens = append(accessTokens, t)
			continue
		}
		if err := h.sessions.DeleteToken(ctx, t.TokenHash); err != nil {
			return 0, fmt.Errorf("%w: %v", errSessionsNotEnded, err)
		}
	}
	for i, t := range accessTokens {
		if err := h.sessions.DeleteToken(ctx, t.TokenHash); err != nil {
			return i, fmt.Errorf("%w: %v", errAccessTokensNotEnded, err)
		}
	}
	return len(accessTokens), nil
}

func isSessionToken(t *domain.AccessToken) bool {
	for _, scope := range strings.Split(t.Scopes, ",") {
		if strings.TrimSpace(scope) == "session" {
			return true
		}
	}
	return false
}

// SetVerifier wires the Studio email verification the verify routes use.
func (h *EmailTokensHandler) SetVerifier(v *EmailVerifier) { h.verifier = v }

// SetPublicLimit replaces the per-client limit on the two public sends.
func (h *EmailTokensHandler) SetPublicLimit(limit func(http.Handler) http.Handler) {
	h.publicLimit = limit
}

// limits returns the public-send and link-confirm limiters. Route tables are
// built from a nil handler in tests; that mounts the routes unlimited.
func (h *EmailTokensHandler) limits() (public, confirm func(http.Handler) http.Handler) {
	if h == nil {
		pass := func(next http.Handler) http.Handler { return next }
		return pass, pass
	}
	return h.publicLimit, h.confirmLimit
}

// allowMailTo spends one of the address's sends. Keyed before any lookup, so
// an unregistered address runs out exactly like a registered one.
func (h *EmailTokensHandler) allowMailTo(address string) bool {
	return h.addressLimit.Allow(strings.ToLower(strings.TrimSpace(address)))
}

// The platform store is Postgres only (no SQLite driver ships), so newer
// statements are plain Postgres literals.
const (
	insertPasswordResetSQL = `INSERT INTO password_resets (user_id, token_hash, expires_at, requested_from) VALUES ($1, $2, $3, $4)`
	// Claiming one link consumes every open link of its account.
	claimPasswordResetSQL = `UPDATE password_resets SET consumed_at = $1
		WHERE consumed_at IS NULL
		  AND user_id = (SELECT user_id FROM password_resets WHERE token_hash = $2 AND consumed_at IS NULL)
		RETURNING token_hash`
)

// rebind converts SQLite-style `?` placeholders to Postgres-style `$1, $2, ...`
// when the underlying driver is Postgres. Sqlite passes the string unchanged.
// Bound to a single connection per handler — pg flag is set once at init.
func (h *EmailTokensHandler) rebind(q string) string {
	if !h.pg {
		return q
	}
	var b []byte
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b = append(b, '$')
			b = append(b, []byte(strconv.Itoa(n))...)
		} else {
			b = append(b, q[i])
		}
	}
	return string(b)
}

func NewEmailTokensHandler(db *sql.DB, sender email.Sender, store emailTokenUsers, studioURL, productName string) *EmailTokensHandler {
	if productName == "" {
		productName = "Excalibase"
	}
	// Detect Postgres driver so we can rewrite `?` placeholders in INSERT
	// statements. We use the driver-name string from sql.DB (set at Open).
	// Both pq and pgx register under "postgres", sqlite as "sqlite3" or "sqlite".
	pg := false
	if db != nil {
		// Probe with a Postgres-specific syntax. If it errors with a
		// recognisable Postgres error (or succeeds), we're on Postgres.
		// SQLite would error with "no such function" instead.
		var n int
		err := db.QueryRow("SELECT 1::int").Scan(&n)
		pg = err == nil
	}
	return &EmailTokensHandler{
		db:              db,
		sender:          sender,
		store:           store,
		studioURL:       strings.TrimRight(studioURL, "/"),
		productName:     productName,
		pg:              pg,
		publicLimit:     middleware.RateLimit(middleware.PerIP, publicSendsPerClient, publicSendWindow),
		confirmLimit:    middleware.RateLimit(middleware.PerIP, confirmsPerClient, confirmWindow),
		addressLimit:    middleware.NewKeyLimiter(publicSendsPerAddress, publicSendWindow),
		runInBackground: func(f func()) { go f() },
	}
}

// Routes mounts the click-link flows. The three public ones are reached by
// someone who cannot log in (or whose credential is the emailed token itself);
// /verify/send is not — it is a send triggered by a logged-in caller, so it
// sits behind RequireAuth and the token's scopes apply, which is what stops a
// read-only credential from sending mail (EXC-418). Extra middleware — the
// per-user rate limit — is passed in by the mount.
func (h *EmailTokensHandler) Routes(r chi.Router, sendLimits ...func(http.Handler) http.Handler) {
	r.With(append([]func(http.Handler) http.Handler{auth.RequireAuth}, sendLimits...)...).
		Post("/verify/send", h.SendVerify)
	publicLimit, confirmLimit := h.limits()
	r.With(confirmLimit).Post("/verify/confirm", h.ConfirmVerify)
	r.With(publicLimit).Post("/verify/resend", h.ResendVerify)
	r.With(publicLimit).Post("/reset/send", h.SendReset)
	r.With(confirmLimit).Post("/reset/confirm", h.ConfirmReset)
}

// --- email verification ---

func (h *EmailTokensHandler) SendVerify(w http.ResponseWriter, r *http.Request) {
	user := auth.GetUser(r.Context())
	if user == nil {
		httpError(w, "auth required", http.StatusUnauthorized)
		return
	}
	if user.Email == "" {
		httpError(w, "user has no email", http.StatusBadRequest)
		return
	}
	if err := h.verifier.Send(r.Context(), user); err != nil {
		writeVerificationSendError(w, err)
		return
	}
	writeJSON(w, map[string]string{"status": "sent"})
}

// ResendVerify mails a fresh link to an account that has not verified yet.
// It is public — the caller cannot sign in — and answers the same for every
// address so it reveals nothing about who is registered.
func (h *EmailTokensHandler) ResendVerify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" {
		httpError(w, "email required", http.StatusBadRequest)
		return
	}
	if !h.verifier.Available() {
		httpError(w, ErrVerificationUnavailable.Error(), http.StatusServiceUnavailable)
		return
	}
	address := strings.TrimSpace(body.Email)
	if h.allowMailTo(address) {
		h.inBackground(func(ctx context.Context) { h.resendVerification(ctx, address) })
	}
	writeJSON(w, map[string]string{"status": "sent"})
}

func (h *EmailTokensHandler) resendVerification(ctx context.Context, address string) {
	user, err := h.store.FindUserByEmail(ctx, address)
	if err != nil {
		log.Printf("ERROR: resend verification lookup: %v", err)
		return
	}
	if user == nil || user.IsService() || user.EmailVerifiedAt != nil {
		return
	}
	if err := h.verifier.Send(ctx, user); err != nil {
		log.Printf("ERROR: resend verification to %s: %v", user.ID, err)
	}
}

// inBackground runs a send detached from the request, so the caller's answer
// never depends on whether the address is registered.
func (h *EmailTokensHandler) inBackground(send func(ctx context.Context)) {
	h.runInBackground(func() {
		ctx, cancel := context.WithTimeout(context.Background(), backgroundMailDeadline)
		defer cancel()
		send(ctx)
	})
}

func writeVerificationSendError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrVerificationUnavailable) {
		httpError(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	log.Printf("ERROR: verification mail: %v", err)
	httpError(w, "failed to send the verification email", http.StatusInternalServerError)
}

func (h *EmailTokensHandler) ConfirmVerify(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" {
		httpError(w, "token required", http.StatusBadRequest)
		return
	}
	userID, err := h.verifier.Confirm(r.Context(), body.Token)
	switch {
	case errors.Is(err, storage.ErrEmailVerificationInvalid):
		httpError(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, ErrVerificationUnavailable):
		httpError(w, err.Error(), http.StatusServiceUnavailable)
		return
	case err != nil:
		log.Printf("ERROR: confirm verification: %v", err)
		httpError(w, "failed to verify the email address", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "verified", "userId": userID})
}

// --- password reset ---

func (h *EmailTokensHandler) SendReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Email == "" {
		httpError(w, "email required", http.StatusBadRequest)
		return
	}
	address := strings.TrimSpace(body.Email)
	if h.allowMailTo(address) {
		ip := clientIP(r)
		h.inBackground(func(ctx context.Context) { h.sendResetLink(ctx, address, ip) })
	}
	writeJSON(w, map[string]any{"status": "sent", "expiresInMinutes": int(passwordResetLifetime.Minutes())})
}

func (h *EmailTokensHandler) sendResetLink(ctx context.Context, address, ip string) {
	user, err := h.store.FindUserByEmail(ctx, address)
	if err != nil {
		log.Printf("ERROR: reset lookup: %v", err)
		return
	}
	if user == nil || user.IsService() || user.Email == "" {
		return
	}
	token, hash, err := mintToken()
	if err != nil {
		log.Printf("ERROR: reset mint token for %s: %v", user.ID, err)
		return
	}
	expires := time.Now().Add(passwordResetLifetime)
	if _, err := h.db.ExecContext(ctx,
		insertPasswordResetSQL,
		user.ID, hash, expires.Format(time.RFC3339), ip); err != nil {
		log.Printf("ERROR: reset store token for %s: %v", user.ID, err)
		return
	}
	msg, err := email.BuildPasswordResetEmail(email.PasswordResetData{
		UserEmail:   user.Email,
		Username:    user.Username,
		ResetURL:    h.studioURL + "/reset-password?" + url.Values{"token": {token}}.Encode(),
		ExpiresMin:  int(passwordResetLifetime.Minutes()),
		ProductName: h.productName,
		IPAddress:   ip,
	})
	if err != nil {
		log.Printf("ERROR: reset build email for %s: %v", user.ID, err)
		return
	}
	msg.To = []string{user.Email}
	msgID, err := h.sender.Send(ctx, msg)
	if err != nil {
		log.Printf("ERROR: reset send to %s: %v", user.ID, err)
		return
	}
	logEmailSent("reset", user.Email, msgID, user.ID)
}

func (h *EmailTokensHandler) ConfirmReset(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"newPassword"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Token == "" || body.NewPassword == "" {
		httpError(w, "token + newPassword required", http.StatusBadRequest)
		return
	}
	if msg := isValidPassword(body.NewPassword); msg != "" {
		httpError(w, msg, http.StatusBadRequest)
		return
	}
	hash := hashToken(body.Token)
	row := h.db.QueryRowContext(r.Context(),
		h.rebind(`SELECT user_id, expires_at, consumed_at FROM password_resets WHERE token_hash = ?`), hash)
	var userID, expiresStr string
	var consumed sql.NullString
	if err := row.Scan(&userID, &expiresStr, &consumed); err != nil {
		httpError(w, "invalid token", http.StatusBadRequest)
		return
	}
	if consumed.Valid {
		httpError(w, "token already used", http.StatusBadRequest)
		return
	}
	expires, _ := time.Parse(time.RFC3339, expiresStr)
	if time.Now().After(expires) {
		httpError(w, "token expired", http.StatusBadRequest)
		return
	}
	// Look up the user so we can call UpdateUserPassword (which keys on
	// username — the existing UserStore convention).
	user, err := h.store.FindUserByID(r.Context(), userID)
	if err != nil || user == nil {
		httpError(w, "user not found", http.StatusBadRequest)
		return
	}
	if h.sessions == nil {
		log.Printf("ERROR: password reset refused: no session store to end existing sessions")
		httpError(w, "password reset is unavailable", http.StatusInternalServerError)
		return
	}
	claimed, err := h.claimReset(r.Context(), hash)
	if err != nil {
		resetFailure(w, "consume token", err)
		return
	}
	if !claimed {
		httpError(w, "token already used", http.StatusBadRequest)
		return
	}
	hashed, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		resetFailure(w, "hash password", err)
		return
	}
	if err := h.store.UpdateUserPassword(r.Context(), user.Username, hashed); err != nil {
		resetFailure(w, "update password", err)
		return
	}
	// After the update, so a sign-in with the old password in between is
	// ended too.
	revoked, err := h.revokeCredentials(r.Context(), user.ID)
	if err != nil {
		log.Printf("ERROR: revoke credentials for %s after reset: %v", user.ID, err)
		msg := "password changed, but existing sessions could not be signed out; reset again"
		if errors.Is(err, errAccessTokensNotEnded) {
			msg = "password changed and sessions signed out, but some access tokens could not be revoked; reset again"
		}
		httpError(w, msg, http.StatusInternalServerError)
		return
	}
	// The reset link reached the account's mailbox, which proves the address.
	if err := h.verifier.MarkVerified(r.Context(), user.ID); err != nil {
		log.Printf("ERROR: mark %s verified after reset: %v", user.ID, err)
	}
	writeJSON(w, map[string]any{"status": "reset", "accessTokensRevoked": revoked, "username": user.Username})
}

// claimReset consumes a reset link, and every other open link of its account,
// before the password changes. Of two confirms racing on one link only one
// claims it.
func (h *EmailTokensHandler) claimReset(ctx context.Context, tokenHash string) (bool, error) {
	rows, err := h.db.QueryContext(ctx,
		claimPasswordResetSQL,
		time.Now().UTC().Format(time.RFC3339), tokenHash)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	claimed := false
	for rows.Next() {
		var consumed string
		if err := rows.Scan(&consumed); err != nil {
			return false, err
		}
		claimed = claimed || consumed == tokenHash
	}
	return claimed, rows.Err()
}

// --- helpers ---

func mintToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	token = hex.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))
	hash = hex.EncodeToString(sum[:])
	return token, hash, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// (clientIP comes from admin.go in this package.)

// logEmailSent emits an audit-friendly log line for every dispatched mail.
// Format is operator-greppable; we explicitly include MessageId so support
// can correlate inbound complaints with the originating send. Recipients
// are logged in full because there's no PII surface here that wasn't
// already on the request — and "we sent X but it never arrived" debugging
// is impossible without the address.
func logEmailSent(kind, recipient, messageID, userID string) {
	log.Printf("INFO: email.sent kind=%s to=%s userId=%s sesMessageId=%s",
		kind, recipient, userID, messageID)
}

// minimum context check so callers can detect missing wiring early
var _ context.Context = context.Background()
