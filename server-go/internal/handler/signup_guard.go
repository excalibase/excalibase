package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/excalibase/provisioning-poc/internal/abuse"
)

// signupRetryAfter is how long a network limited by the sign-up budget waits (seconds).
const signupRetryAfter = "600"

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

// oauthSignupRefusal words a refused provider sign-up as a code Studio's login page shows.
func oauthSignupRefusal(err error) error {
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

// checkOAuthEmail is asked before an invite or the budget, so a refused address costs neither.
func (h *AuthHandler) checkOAuthEmail(ctx context.Context, address string) error {
	return oauthSignupRefusal(h.signupGuard.CheckEmail(ctx, address))
}

// spendOAuthSignup takes one sign-up from the network's budget.
func (h *AuthHandler) spendOAuthSignup(ctx context.Context) error {
	addr, _ := ctx.Value(clientAddrKey{}).(string)
	if h.signupGuard.AllowSignup(ctx, addr) {
		return nil
	}
	return oauthSignupRefusal(abuse.ErrSignupRateLimited)
}
