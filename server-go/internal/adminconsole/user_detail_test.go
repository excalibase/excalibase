package adminconsole

import (
	"context"
	"errors"
	"testing"
)

type fakeDetailReader struct {
	detail *UserDetail
	err    error
}

func (f fakeDetailReader) ReadUserDetail(_ context.Context, _ string) (*UserDetail, error) {
	return f.detail, f.err
}

func TestUserDetailFlagsATempMailAddressWhenAListIsWired(t *testing.T) {
	reader := fakeDetailReader{detail: &UserDetail{Profile: UserProfile{Email: "a@mailinator.test"}}}
	plain, err := NewUserDetails(reader, nil).Detail(context.Background(), "u1")
	if err != nil || plain.Profile.TempMail != nil {
		t.Fatalf("without a list the flag stays unknown: %+v, %v", plain.Profile.TempMail, err)
	}
	reader = fakeDetailReader{detail: &UserDetail{Profile: UserProfile{Email: "a@mailinator.test"}}}
	flagged, err := NewUserDetails(reader, func(email string) bool { return email == "a@mailinator.test" }).Detail(context.Background(), "u1")
	if err != nil || flagged.Profile.TempMail == nil || !*flagged.Profile.TempMail {
		t.Fatalf("temp mail not flagged: %+v, %v", flagged.Profile.TempMail, err)
	}
}

func TestUserDetailKeepsNotFoundAndNeverReturnsNilLists(t *testing.T) {
	if _, err := NewUserDetails(fakeDetailReader{err: ErrUserNotFound}, nil).Detail(context.Background(), "nope"); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
	got, err := NewUserDetails(fakeDetailReader{detail: &UserDetail{}}, nil).Detail(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Orgs == nil || got.Projects == nil || got.SignIn == nil || got.Suspensions == nil || got.Audit == nil || got.TrustHistory == nil {
		t.Fatalf("lists must be empty, not null, for the page: %+v", got)
	}
}

func TestUserDetailRefusesAnEmptyID(t *testing.T) {
	if _, err := NewUserDetails(fakeDetailReader{}, nil).Detail(context.Background(), " "); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("err = %v, want ErrUserNotFound", err)
	}
}
