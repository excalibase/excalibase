package handler

import (
	"context"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/auth"
)

// creditTermsCookie carries the credit-terms box a person ticked before a
// Google or GitHub sign-in through the provider's round trip.
const (
	creditTermsCookie = "excali_credit_terms"
	creditTermsParam  = "creditTerms"
	creditTermsMaxAge = 600
)

// CreditSignup is what sign-up and verification tell the credit ledger (EXC-866).
// It is nil while credits are not wired; every method also answers nothing while the flag is off.
type CreditSignup interface {
	// RecordTerms stores that the new account ticked the credit terms.
	RecordTerms(ctx context.Context, userID, version string)
	// GrantOnVerified tries the sign-up credit once the address is proven. It is
	// idempotent, and the sweeper is its backstop.
	GrantOnVerified(ctx context.Context, userID string)
}

// SetCreditSignup wires the credit hooks for sign-up and OAuth sign-in.
func (h *AuthHandler) SetCreditSignup(signup CreditSignup) { h.creditSignup = signup }

// SetCreditSignup grants after a link or an admin proves an address.
func (v *EmailVerifier) SetCreditSignup(signup CreditSignup) { v.creditSignup = signup }

func (v *EmailVerifier) granted(ctx context.Context, userID string) {
	if v.creditSignup != nil {
		v.creditSignup.GrantOnVerified(ctx, userID)
	}
}

func (h *AuthHandler) recordCreditTerms(ctx context.Context, userID, version string) {
	if h.creditSignup != nil && version != "" {
		h.creditSignup.RecordTerms(ctx, userID, version)
	}
}

// rememberCreditTerms keeps the version the person ticked until the provider sends them back.
func rememberCreditTerms(w http.ResponseWriter, r *http.Request) {
	if version := r.URL.Query().Get(creditTermsParam); version != "" {
		http.SetCookie(w, auth.NewCookie(creditTermsCookie, version, creditTermsMaxAge, http.SameSiteLaxMode))
	}
}

// takeCreditTerms reads and clears the remembered version.
func takeCreditTerms(w http.ResponseWriter, r *http.Request) string {
	version, _ := auth.ReadCookie(r, creditTermsCookie)
	if version != "" {
		http.SetCookie(w, auth.NewCookie(creditTermsCookie, "", -1, http.SameSiteLaxMode))
	}
	return version
}
