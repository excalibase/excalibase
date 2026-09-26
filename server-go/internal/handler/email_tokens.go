package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
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
// We're intentionally minimal here — no rate-limit on /send (the
// existing per-IP middleware covers it) and no captcha. Both can be
// added in v1.2 once we see abuse signals.
type EmailTokensHandler struct {
	db          *sql.DB         // platform-db, holds password_resets
	sender      email.Sender    // SES, Resend, or noop in dev
	store       emailTokenUsers // user lookup for verify/reset
	studioURL   string          // Studio origin the click links open
	productName string
	pg          bool // true if backing db is Postgres (uses $1 placeholders)
	verifier    *EmailVerifier
	publicLimit func(http.Handler) http.Handler
}

// emailTokenUsers is the slice of the user store the click-link flows need.
type emailTokenUsers interface {
	FindUserByEmail(ctx context.Context, email string) (*domain.User, error)
	FindUserByID(ctx context.Context, id string) (*domain.User, error)
	UpdateUserPassword(ctx context.Context, username, passwordHash string) error
}

// SetVerifier wires the Studio email verification the verify routes use.
func (h *EmailTokensHandler) SetVerifier(v *EmailVerifier) { h.verifier = v }

func (h *EmailTokensHandler) resendLimit(next http.Handler) http.Handler {
	if h == nil || h.publicLimit == nil {
		return next
	}
	return h.publicLimit(next)
}

// SetPublicLimit rate-limits the public resend route, which anyone may call.
func (h *EmailTokensHandler) SetPublicLimit(limit func(http.Handler) http.Handler) {
	h.publicLimit = limit
}

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
		db:          db,
		sender:      sender,
		store:       store,
		studioURL:   strings.TrimRight(studioURL, "/"),
		productName: productName,
		pg:          pg,
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
	r.Post("/verify/confirm", h.ConfirmVerify)
	r.With(h.resendLimit).Post("/verify/resend", h.ResendVerify)
	r.Post("/reset/send", h.SendReset)
	r.Post("/reset/confirm", h.ConfirmReset)
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
	user, err := h.store.FindUserByEmail(r.Context(), body.Email)
	if err != nil {
		log.Printf("ERROR: resend verification lookup: %v", err)
	}
	if user != nil && !user.IsService() && user.EmailVerifiedAt == nil {
		if err := h.verifier.Send(r.Context(), user); err != nil {
			log.Printf("ERROR: resend verification to %s: %v", user.ID, err)
		}
	}
	writeJSON(w, map[string]string{"status": "sent"})
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
	user, err := h.store.FindUserByEmail(r.Context(), body.Email)
	if err != nil || user == nil {
		// Always 200 to avoid disclosing whether an email is registered.
		writeJSON(w, map[string]string{"status": "sent"})
		return
	}
	token, hash, err := mintToken()
	if err != nil {
		httpError(w, "mint token: "+safeError(err), http.StatusInternalServerError)
		return
	}
	expires := time.Now().Add(time.Hour)
	ip := clientIP(r)
	if _, err := h.db.ExecContext(r.Context(),
		h.rebind(`INSERT INTO password_resets (user_id, token_hash, expires_at, requested_from) VALUES (?, ?, ?, ?)`),
		user.ID, hash, expires.Format(time.RFC3339), ip); err != nil {
		httpError(w, "store token: "+safeError(err), http.StatusInternalServerError)
		return
	}
	msg, err := email.BuildPasswordResetEmail(email.PasswordResetData{
		UserEmail:   body.Email,
		ResetURL:    h.studioURL + "/reset-password?" + url.Values{"token": {token}}.Encode(),
		ExpiresMin:  60,
		ProductName: h.productName,
		IPAddress:   ip,
	})
	if err != nil {
		httpError(w, "build email: "+safeError(err), http.StatusInternalServerError)
		return
	}
	msg.To = []string{body.Email}
	msgID, err := h.sender.Send(r.Context(), msg)
	if err != nil {
		httpError(w, "send: "+safeError(err), http.StatusInternalServerError)
		return
	}
	logEmailSent("reset", body.Email, msgID, user.ID)
	writeJSON(w, map[string]string{"status": "sent"})
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
	hashed, err := auth.HashPassword(body.NewPassword)
	if err != nil {
		httpError(w, "hash: "+safeError(err), http.StatusInternalServerError)
		return
	}
	if err := h.store.UpdateUserPassword(r.Context(), user.Username, hashed); err != nil {
		httpError(w, "update password: "+safeError(err), http.StatusInternalServerError)
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		h.rebind(`UPDATE password_resets SET consumed_at = ? WHERE token_hash = ?`),
		time.Now().UTC().Format(time.RFC3339), hash); err != nil {
		log.Printf("ERROR: consume reset token for %s: %v", user.ID, err)
	}
	// The reset link reached the account's mailbox, which proves the address.
	if err := h.verifier.MarkVerified(r.Context(), user.ID); err != nil {
		log.Printf("ERROR: mark %s verified after reset: %v", user.ID, err)
	}
	writeJSON(w, map[string]string{"status": "reset"})
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
