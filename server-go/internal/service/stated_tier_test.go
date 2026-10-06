package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// EXC-555: a body may state the plan it expects; it is accepted only when it
// is the organisation's plan, since the organisation is what sizes a project.
func TestConfirmStatedTier(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	setOrgTier(svc, "org2", domain.Standard)
	cases := []struct {
		name, org, stated string
		refused           bool
	}{
		{"same plan", "org1", "FREE", false},
		{"same plan in another case", "org1", "free", false},
		{"standard org says standard", "org2", "Standard", false},
		{"a bigger plan", "org1", "ENTERPRISE", true},
		{"a smaller plan", "org2", "FREE", true},
		{"an empty plan", "org1", "", true},
		{"a made-up plan", "org1", "GOLD", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.ConfirmStatedTier(context.Background(), tc.org, tc.stated)
			if tc.refused != errors.Is(err, ErrTierNotOrgPlan) {
				t.Fatalf("err = %v, refused want %v", err, tc.refused)
			}
		})
	}
}

func TestConfirmStatedTierNamesTheOrganisationsPlan(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	err := svc.ConfirmStatedTier(context.Background(), "org1", "ENTERPRISE")
	want := "a project's plan comes from its organization (FREE); change the organization's plan instead"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestConfirmStatedTierForAnUnknownOrganisationIsUnresolved(t *testing.T) {
	svc, _, _ := setupProvisioningTest(t)
	err := svc.ConfirmStatedTier(context.Background(), "no-such-org", "FREE")
	if !errors.Is(err, ErrOrgTierUnresolved) {
		t.Fatalf("err = %v, want ErrOrgTierUnresolved", err)
	}
}
