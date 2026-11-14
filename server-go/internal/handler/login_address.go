package handler

import (
	"context"
	"github.com/excalibase/provisioning-poc/internal/loginguard"
	"net/netip"
	"time"
)

// loginAddress is what a sign-in budget counts: an IPv4 address as it is, an
// IPv6 address as its /64, since one host holds the whole range.
func loginAddress(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil || addr.Is4() || addr.Is4In6() {
		return ip
	}
	prefix, err := addr.Prefix(64)
	if err != nil {
		return ip
	}
	return prefix.String()
}

// SetLoginSleep replaces how a delayed sign-in waits; tests use it to record delays.
func (h *AuthHandler) SetLoginSleep(sleep func(context.Context, time.Duration) error) {
	h.sleep = sleep
}

// sleepContext waits d, or until the request is gone.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SetAccountFailureCap sets how many failed sign-ins one account takes per window before it refuses all attempts.
func (h *AuthHandler) SetAccountFailureCap(limit int) {
	h.accountSlowdown = newAccountSlowdown(limit)
}

// unknownNameMemoryShare is the part of the container's memory that sign-in
// counters for names matching no account may use.
const unknownNameMemoryShare = 0.03

func newAccountSlowdown(failureCap int) *loginguard.Slowdown {
	bound := loginguard.UnknownNameBound(loginguard.ContainerMemoryLimit(), unknownNameMemoryShare)
	return loginguard.NewSlowdown(accountFreeFailures, accountDelayStep, accountDelayCap, failureCap, defaultLoginWindow, bound)
}
