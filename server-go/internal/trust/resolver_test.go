package trust

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

const unknownProject = "proj-x"

type fakeStore struct {
	records map[string]*Record
	err     error
	finds   int
}

func (f *fakeStore) Find(_ context.Context, userID string) (*Record, error) {
	f.finds++
	if f.err != nil {
		return nil, f.err
	}
	return f.records[userID], nil
}

func (f *fakeStore) Save(context.Context, Record, string) error { return nil }

type fakeInstances struct {
	byProject map[string]*domain.DatabaseInstance
	err       error
	lookups   int
	seenCtx   context.Context
}

func (f *fakeInstances) FindByProjectIDContext(ctx context.Context, projectID string) (*domain.DatabaseInstance, error) {
	f.lookups++
	f.seenCtx = ctx
	if f.err != nil {
		return nil, f.err
	}
	return f.byProject[projectID], nil
}

type fakeOrgs struct {
	byID map[string]*domain.Org
	err  error
}

func (f *fakeOrgs) FindOrgByID(_ context.Context, id string) (*domain.Org, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byID[id], nil
}

type fixture struct {
	store     *fakeStore
	instances *fakeInstances
	orgs      *fakeOrgs
}

func newFixture() *fixture {
	return &fixture{
		store:     &fakeStore{records: map[string]*Record{}},
		instances: &fakeInstances{byProject: map[string]*domain.DatabaseInstance{}},
		orgs:      &fakeOrgs{byID: map[string]*domain.Org{}},
	}
}

func (f *fixture) resolver(flags features.Flags) *Resolver {
	return NewResolver(flags, f.store, f.instances, f.orgs)
}

func (f *fixture) project(projectID, orgID, ownerID string) {
	f.instances.byProject[projectID] = &domain.DatabaseInstance{ProjectID: projectID, OrgID: orgID}
	f.orgs.byID[orgID] = &domain.Org{ID: orgID, OwnerID: ownerID}
}

func on() features.Flags  { return features.NewStatic(features.LimitedTrial) }
func off() features.Flags { return features.NewStatic() }

func TestFlagOffEveryoneIsFullAndNothingIsRead(t *testing.T) {
	f := newFixture()
	f.store.err = errors.New("must not be asked")
	for name, flags := range map[string]features.Flags{"off": off(), "nil": nil} {
		r := f.resolver(flags)
		if state, err := r.ForOwner(context.Background(), "nobody"); state != Full || err != nil {
			t.Errorf("%s ForOwner: %s %v", name, state, err)
		}
		if state, err := r.ForOwner(context.Background(), ""); state != Full || err != nil {
			t.Errorf("%s ForOwner empty: %s %v", name, state, err)
		}
		if state, err := r.ForProject(context.Background(), unknownProject); state != Full || err != nil {
			t.Errorf("%s ForProject: %s %v", name, state, err)
		}
	}
	if f.store.finds != 0 || f.instances.lookups != 0 {
		t.Errorf("flag off still read: finds=%d lookups=%d", f.store.finds, f.instances.lookups)
	}
}

func TestFlagOnOwnerStates(t *testing.T) {
	f := newFixture()
	f.store.records["full-user"] = &Record{UserID: "full-user", State: Full}
	f.store.records["limited-user"] = &Record{UserID: "limited-user", State: Limited}
	f.store.records["garbage-user"] = &Record{UserID: "garbage-user", State: State("bogus")}
	r := f.resolver(on())
	cases := []struct {
		owner string
		want  State
	}{
		{"full-user", Full},
		{"limited-user", Limited},
		{"no-row-user", Limited},
	}
	for _, tc := range cases {
		state, err := r.ForOwner(context.Background(), tc.owner)
		if state != tc.want || err != nil {
			t.Errorf("%s: got %s %v, want %s", tc.owner, state, err, tc.want)
		}
	}
}

// A stored state nobody wrote through Save is corruption: limited, and the caller is told.
func TestAnUnknownStoredStateIsLimitedWithAnError(t *testing.T) {
	f := newFixture()
	f.store.records["garbage-user"] = &Record{UserID: "garbage-user", State: State("bogus")}
	state, err := f.resolver(on()).ForOwner(context.Background(), "garbage-user")
	if state != Limited || !errors.Is(err, ErrInvalidRecord) {
		t.Errorf("got %s %v, want limited and ErrInvalidRecord", state, err)
	}
}

// Wrapped causes stay reachable, so a caller can tell a timeout from a missing row.
func TestResolverErrorsKeepTheirCause(t *testing.T) {
	cause := errors.New("db down")
	f := newFixture()
	f.store.err = cause
	if _, err := f.resolver(on()).ForOwner(context.Background(), "u"); !errors.Is(err, cause) {
		t.Errorf("owner read lost its cause: %v", err)
	}
	f = newFixture()
	f.instances.err = cause
	if _, err := f.resolver(on()).ForProject(context.Background(), unknownProject); !errors.Is(err, cause) || !errors.Is(err, ErrUnresolved) {
		t.Errorf("project lookup lost a cause: %v", err)
	}
	f = newFixture()
	f.project(unknownProject, "org-x", "u")
	f.orgs.err = cause
	if _, err := f.resolver(on()).ForProject(context.Background(), unknownProject); !errors.Is(err, cause) || !errors.Is(err, ErrUnresolved) {
		t.Errorf("org lookup lost a cause: %v", err)
	}
}

func TestFlagOnFailsClosed(t *testing.T) {
	f := newFixture()
	f.store.records["u"] = &Record{UserID: "u", State: Full}
	r := f.resolver(on())
	if state, err := r.ForOwner(context.Background(), ""); state != Limited || !errors.Is(err, ErrUnresolved) {
		t.Errorf("empty owner: %s %v", state, err)
	}
	f.store.err = errors.New("db down")
	if state, err := r.ForOwner(context.Background(), "u"); state != Limited || err == nil {
		t.Errorf("store error: %s %v", state, err)
	}
}

func TestForProjectUsesTheOrgOwnerNotAnyoneElse(t *testing.T) {
	f := newFixture()
	f.project("proj-a", "org-1", "owner-full")
	f.project("proj-b", "org-2", "owner-limited")
	f.store.records["owner-full"] = &Record{UserID: "owner-full", State: Full}
	f.store.records["owner-limited"] = &Record{UserID: "owner-limited", State: Limited}
	// A limited member of a full owner's org runs at full: only OwnerID counts.
	f.store.records["member"] = &Record{UserID: "member", State: Limited}
	r := f.resolver(on())
	if state, err := r.ForProject(context.Background(), "proj-a"); state != Full || err != nil {
		t.Errorf("proj-a: %s %v", state, err)
	}
	if state, err := r.ForProject(context.Background(), "proj-b"); state != Limited || err != nil {
		t.Errorf("proj-b: %s %v", state, err)
	}
}

func TestForProjectFailsClosedWhenTheChainBreaks(t *testing.T) {
	cases := map[string]func(f *fixture){
		"unknown project": func(f *fixture) {},
		"no org on project": func(f *fixture) {
			f.instances.byProject[unknownProject] = &domain.DatabaseInstance{ProjectID: unknownProject}
		},
		"org missing": func(f *fixture) {
			f.instances.byProject[unknownProject] = &domain.DatabaseInstance{ProjectID: unknownProject, OrgID: "gone"}
		},
		"org without owner": func(f *fixture) { f.project(unknownProject, "org-x", "") },
		"instance lookup fails": func(f *fixture) {
			f.project(unknownProject, "org-x", "u")
			f.instances.err = errors.New("db down")
		},
		"org lookup fails": func(f *fixture) {
			f.project(unknownProject, "org-x", "u")
			f.orgs.err = errors.New("db down")
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			f.store.records["u"] = &Record{UserID: "u", State: Full}
			setup(f)
			state, err := f.resolver(on()).ForProject(context.Background(), unknownProject)
			if state != Limited || !errors.Is(err, ErrUnresolved) {
				t.Errorf("got %s %v, want limited and ErrUnresolved", state, err)
			}
		})
	}
}

func TestRecordValidate(t *testing.T) {
	valid := Record{UserID: "u", State: Limited, Method: MethodGitHub, Reason: ReasonUnverified}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid record refused: %v", err)
	}
	bad := map[string]func(r *Record){
		"no user":        func(r *Record) { r.UserID = "" },
		"unknown state":  func(r *Record) { r.State = "maybe" },
		"unknown method": func(r *Record) { r.Method = "bribe" },
		"unknown reason": func(r *Record) { r.Reason = "because" },
		"empty state":    func(r *Record) { r.State = "" },
	}
	for name, mutate := range bad {
		rec := valid
		mutate(&rec)
		if err := rec.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

type ctxKey struct{}

// A cancelled request must reach the lookup, not be swapped for a background context.
func TestForProjectPassesTheCallersContextToTheLookup(t *testing.T) {
	f := newFixture()
	f.project(unknownProject, "org-x", "u")
	ctx := context.WithValue(context.Background(), ctxKey{}, "caller")
	if _, err := f.resolver(on()).ForProject(ctx, unknownProject); err != nil {
		t.Fatal(err)
	}
	if f.instances.seenCtx == nil || f.instances.seenCtx.Value(ctxKey{}) != "caller" {
		t.Error("the project lookup did not get the caller's context")
	}
}
