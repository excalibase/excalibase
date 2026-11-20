package adminconsole

import (
	"context"
	"errors"
	"testing"
)

type recordingReader struct {
	fakeReader
	users  int
	orgs   int
	usersQ UserQuery
	orgsQ  OrgQuery
}

func (r *recordingReader) ListUsers(_ context.Context, q UserQuery) (Page[UserRow], error) {
	r.users++
	r.usersQ = q
	return Page[UserRow]{Items: []UserRow{}}, nil
}

func (r *recordingReader) ListOrgs(_ context.Context, q OrgQuery) (Page[OrgRow], error) {
	r.orgs++
	r.orgsQ = q
	return Page[OrgRow]{Items: []OrgRow{}}, nil
}

func TestUsersRefuseASortOutsideTheAllowListWithoutAskingTheStore(t *testing.T) {
	for _, sort := range []string{"password_hash", "email; DROP TABLE users", "u.email", "created DESC"} {
		reader := &recordingReader{}
		_, err := NewConsole(reader, nil, nil).Users(context.Background(), UserQuery{Sort: sort})
		if !errors.Is(err, ErrInvalidQuery) || reader.users != 0 {
			t.Errorf("sort %q: err=%v store calls=%d", sort, err, reader.users)
		}
	}
}

func TestUsersAcceptEveryAllowedSortAndFilter(t *testing.T) {
	for _, sort := range []string{"", "created", "email", "lastLogin", "orgs", "projects", "apps"} {
		reader := &recordingReader{}
		if _, err := NewConsole(reader, nil, nil).Users(context.Background(), UserQuery{Sort: sort, Status: "active", Role: "user"}); err != nil {
			t.Errorf("sort %q: %v", sort, err)
		}
	}
}

func TestUsersRefuseAnUnknownStatusOrRole(t *testing.T) {
	for _, query := range []UserQuery{{Status: "gone"}, {Role: "root"}} {
		reader := &recordingReader{}
		if _, err := NewConsole(reader, nil, nil).Users(context.Background(), query); !errors.Is(err, ErrInvalidQuery) || reader.users != 0 {
			t.Errorf("%+v: err=%v calls=%d", query, err, reader.users)
		}
	}
}

func TestAskingForSuspendedUsersShowsThem(t *testing.T) {
	reader := &recordingReader{}
	if _, err := NewConsole(reader, nil, nil).Users(context.Background(), UserQuery{Status: StatusSuspended}); err != nil {
		t.Fatal(err)
	}
	if !reader.usersQ.IncludeSuspended {
		t.Fatal("a suspended filter on a list that hides suspended accounts would always be empty")
	}
}

func TestOrgsRefuseASortOutsideTheAllowList(t *testing.T) {
	for _, sort := range []string{"owner_id", "name;--", "o.tier"} {
		reader := &recordingReader{}
		_, err := NewConsole(reader, nil, nil).Orgs(context.Background(), OrgQuery{Sort: sort})
		if !errors.Is(err, ErrInvalidQuery) || reader.orgs != 0 {
			t.Errorf("sort %q: err=%v calls=%d", sort, err, reader.orgs)
		}
	}
}

func TestOrgsAcceptEveryAllowedSortAndFilter(t *testing.T) {
	for _, sort := range []string{"", "created", "name", "owner", "plan", "projects", "apps"} {
		reader := &recordingReader{}
		if _, err := NewConsole(reader, nil, nil).Orgs(context.Background(), OrgQuery{Sort: sort, Tier: "standard", OwnerStatus: StatusActive}); err != nil {
			t.Errorf("sort %q: %v", sort, err)
		}
		if reader.orgsQ.Tier != "STANDARD" {
			t.Errorf("tier must reach the store in the stored case, got %q", reader.orgsQ.Tier)
		}
	}
}

func TestOrgsRefuseAnUnknownTierOrOwnerStatus(t *testing.T) {
	for _, query := range []OrgQuery{{Tier: "gold"}, {OwnerStatus: "banned"}} {
		reader := &recordingReader{}
		if _, err := NewConsole(reader, nil, nil).Orgs(context.Background(), query); !errors.Is(err, ErrInvalidQuery) || reader.orgs != 0 {
			t.Errorf("%+v: err=%v calls=%d", query, err, reader.orgs)
		}
	}
}

func TestOrgsAskingForSuspendedOwnersShowsThem(t *testing.T) {
	reader := &recordingReader{}
	if _, err := NewConsole(reader, nil, nil).Orgs(context.Background(), OrgQuery{OwnerStatus: StatusSuspended}); err != nil {
		t.Fatal(err)
	}
	if !reader.orgsQ.IncludeSuspended {
		t.Fatal("suspended owners must be included when asked for")
	}
}
