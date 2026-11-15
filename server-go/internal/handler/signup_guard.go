package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/abuse"
	"github.com/excalibase/provisioning-poc/internal/clientaddr"
)

// signupRetryAfter is how long a network limited by the sign-up budget waits (seconds).
const signupRetryAfter = "600"

// admitSignup answers a refused sign-up and reports false; the guard checks the
// network's budget and then the address (EXC-755).
func (h *AuthHandler) admitSignup(w http.ResponseWriter, r *http.Request, address string) bool {
	err := h.signupGuard.Admit(r.Context(), clientaddr.FromRequest(r), address)
	if err == nil {
		return true
	}
	writeSignupRefusal(w, err)
	return false
}

func writeSignupRefusal(w http.ResponseWriter, err error) {
	var refusal *abuse.EmailRefusal
	switch {
	case errors.Is(err, abuse.ErrSignupRateLimited):
		w.Header().Set(headerRetryAfter, signupRetryAfter)
		httpError(w, err.Error(), http.StatusTooManyRequests)
	case errors.As(err, &refusal):
		httpError(w, refusal.Error(), refusal.Status())
	default:
		httpError(w, "sign-up is unavailable; try again", http.StatusServiceUnavailable)
	}
}

type clientAddrKey struct{}

func withClientAddr(ctx context.Context, addr string) context.Context {
	return context.WithValue(ctx, clientAddrKey{}, addr)
}

// admitOAuthSignup is the guard for an account a provider sign-in would create;
// the refusal is a code Studio's login page words.
func (h *AuthHandler) admitOAuthSignup(ctx context.Context, address string) error {
	addr, _ := ctx.Value(clientAddrKey{}).(string)
	err := h.signupGuard.Admit(ctx, addr, address)
	var refusal *abuse.EmailRefusal
	switch {
	case err == nil:
		return nil
	case errors.Is(err, abuse.ErrSignupRateLimited):
		return signInRefusal("signup_rate_limited")
	case errors.As(err, &refusal) && refusal.Kind == abuse.KindTryAgain:
		return signInRefusal("email_try_again")
	default:
		return signInRefusal("email_not_allowed")
	}
}
