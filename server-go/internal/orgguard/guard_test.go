package orgguard_test

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func guard(suspended map[string]bool, lookupErr error) *orgguard.Guard {
	instances := fakestore.NewInstances()
	instances.Items["p1"] = &domain.DatabaseInstance{ProjectID: "p1", OrgID: "o1"}
	return orgguard.New(instances, func(context.Context) (map[string]bool, error) { return suspended, lookupErr })
}

func TestCheckProject(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		g       *orgguard.Guard
		ctx     context.Context
		project string
		want    error
	}{
		{"suspended owner", guard(map[string]bool{"o1": true}, nil), ctx, "p1", orgguard.ErrOrgSuspended},
		{"another org suspended", guard(map[string]bool{"o2": true}, nil), ctx, "p1", nil},
		{"platform admin", guard(map[string]bool{"o1": true}, nil), orgguard.WithPlatformOverride(ctx), "p1", nil},
		{"unknown project", guard(map[string]bool{"o1": true}, nil), ctx, "missing", nil},
		{"no guard wired", nil, ctx, "p1", nil},
	}
	for _, tc := range cases {
		if err := tc.g.CheckProject(tc.ctx, tc.project); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestCheckProject_ALookupFailureRefuses(t *testing.T) {
	err := guard(nil, errors.New("db down")).CheckProject(context.Background(), "p1")
	if err == nil || errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("err = %v, want a lookup failure that still refuses", err)
	}
}

func TestMessageIsPlain(t *testing.T) {
	if orgguard.ErrOrgSuspended.Error() != "This organisation is suspended. Contact support." {
		t.Fatalf("message = %q", orgguard.ErrOrgSuspended.Error())
	}
}
