package abuse

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

func TestSignupNetworkKey_GroupsIPv4By24AndIPv6By64(t *testing.T) {
	same := [][2]string{
		{"203.0.113.5", "203.0.113.250"},
		{"2001:db8:1:2::1", "2001:db8:1:2:ffff:ffff:ffff:ffff"},
		{"::ffff:203.0.113.9", "203.0.113.77"},
	}
	for _, pair := range same {
		if SignupNetworkKey(pair[0]) != SignupNetworkKey(pair[1]) {
			t.Errorf("%s and %s are separate networks", pair[0], pair[1])
		}
	}
	different := [][2]string{
		{"203.0.113.5", "203.0.114.5"},
		{"2001:db8:1:2::1", "2001:db8:1:3::1"},
	}
	for _, pair := range different {
		if SignupNetworkKey(pair[0]) == SignupNetworkKey(pair[1]) {
			t.Errorf("%s and %s share a network", pair[0], pair[1])
		}
	}
	if SignupNetworkKey("not an ip") == "" {
		t.Error("an unparseable address got an empty key, which would disable the limit")
	}
}

func signupGuard(t *testing.T, perHour int, flags ...features.Feature) *SignupGuard {
	t.Helper()
	return NewSignupGuard(features.NewStatic(flags...), NewEmailPolicy(newDNS()), perHour)
}

func TestSignupGuard_LimitsPerNetworkPerHour(t *testing.T) {
	guard := signupGuard(t, 3, features.SignupGuard)
	ctx := context.Background()
	for i := range 3 {
		if !guard.AllowSignup(ctx, "203.0.113.1") {
			t.Fatalf("sign-up %d refused inside the limit", i+1)
		}
	}
	if guard.AllowSignup(ctx, "203.0.113.200") {
		t.Error("a 4th sign-up from the same /24 was allowed")
	}
	if !guard.AllowSignup(ctx, "203.0.114.1") {
		t.Error("another /24 was limited by the first")
	}
}

func TestSignupGuard_OffFlagAllowsEverything(t *testing.T) {
	guard := signupGuard(t, 1)
	ctx := context.Background()
	for range 5 {
		if !guard.AllowSignup(ctx, "203.0.113.1") {
			t.Fatal("limited while the flag is off")
		}
	}
	if err := guard.CheckEmail(ctx, "a@fldubai.com"); err != nil {
		t.Errorf("email refused while the flag is off: %v", err)
	}
}

func TestSignupGuard_ChecksEmailWhenOn(t *testing.T) {
	guard := signupGuard(t, 5, features.SignupGuard)
	if err := guard.CheckEmail(context.Background(), "a@fldubai.com"); !errors.Is(err, ErrEmailRefused) {
		t.Errorf("fldubai.com: %v", err)
	}
	if err := guard.CheckEmail(context.Background(), "dev@gmail.com"); err != nil {
		t.Errorf("gmail.com: %v", err)
	}
	var none *SignupGuard
	if err := none.CheckEmail(context.Background(), "a@fldubai.com"); err != nil || !none.AllowSignup(context.Background(), "1.2.3.4") {
		t.Error("a nil guard must allow everything")
	}
}
