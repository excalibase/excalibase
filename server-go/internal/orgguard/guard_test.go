package orgguard_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

type failingInstances struct{ *fakestore.Instances }

func (failingInstances) FindByProjectID(string) (*domain.DatabaseInstance, error) {
	return nil, errors.New("db down")
}

// Mutant O1: a project that cannot be read must refuse, not pass as unknown.
func TestCheckProject_AFailedProjectLookupRefuses(t *testing.T) {
	g := orgguard.New(failingInstances{fakestore.NewInstances()}, func(context.Context) (map[string]bool, error) { return nil, nil })
	err := g.CheckProject(context.Background(), "p1")
	if !errors.Is(err, orgguard.ErrCheckUnavailable) {
		t.Fatalf("err = %v, want ErrCheckUnavailable", err)
	}
}

func TestCheckProject_ALookupFailureIsMarkedUnavailable(t *testing.T) {
	err := guard(nil, errors.New("db down")).CheckProject(context.Background(), "p1")
	if !errors.Is(err, orgguard.ErrCheckUnavailable) || !orgguard.IsRefusal(err) {
		t.Fatalf("err = %v, want ErrCheckUnavailable", err)
	}
}

// A suspension stops the apps of projects the account created as well as the
// orgs it owns, so the guard refuses both.
func TestCheckProject_AProjectCreatedByASuspendedMemberIsRefused(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Items["p1"] = &domain.DatabaseInstance{ProjectID: "p1", OrgID: "someone-elses-org", OwnerID: "suspended-member"}
	g := orgguard.New(instances, func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }).
		WithSuspendedUsers(func(context.Context) (map[string]bool, error) { return map[string]bool{"suspended-member": true}, nil })

	if err := g.CheckProject(context.Background(), "p1"); !errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("err = %v, want ErrOrgSuspended", err)
	}
	if err := g.CheckProject(orgguard.WithPlatformOverride(context.Background()), "p1"); err != nil {
		t.Fatalf("platform admin: %v", err)
	}
	instances.Items["p1"].OwnerID = "someone-else"
	if err := g.CheckProject(context.Background(), "p1"); err != nil {
		t.Fatalf("a project of an unsuspended creator: %v", err)
	}
}

func TestCheckProject_AHangingLookupIsBounded(t *testing.T) {
	g := orgguard.New(fakestore.NewInstances(), func(ctx context.Context) (map[string]bool, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	instances := fakestore.NewInstances()
	instances.Items["p1"] = &domain.DatabaseInstance{ProjectID: "p1", OrgID: "o1"}
	g = orgguard.New(instances, func(ctx context.Context) (map[string]bool, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}).WithLookupTimeout(20 * time.Millisecond)

	start := time.Now()
	err := g.CheckProject(context.Background(), "p1")
	if !errors.Is(err, orgguard.ErrCheckUnavailable) || time.Since(start) > 2*time.Second {
		t.Fatalf("err = %v after %v, want a bounded ErrCheckUnavailable", err, time.Since(start))
	}
}

// Mutant D9: a failed read of the suspended-users list must refuse, not let the request through.
func TestCheckProject_AFailedCreatorLookupRefuses(t *testing.T) {
	instances := fakestore.NewInstances()
	instances.Items["p1"] = &domain.DatabaseInstance{ProjectID: "p1", OrgID: "o1", OwnerID: "creator"}
	g := orgguard.New(instances, func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }).
		WithSuspendedUsers(func(context.Context) (map[string]bool, error) { return nil, errors.New("db down") })

	err := g.CheckProject(context.Background(), "p1")
	if !errors.Is(err, orgguard.ErrCheckUnavailable) || errors.Is(err, orgguard.ErrOrgSuspended) {
		t.Fatalf("err = %v, want ErrCheckUnavailable", err)
	}
}
