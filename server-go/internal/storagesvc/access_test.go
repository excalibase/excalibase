package storagesvc

import (
	"errors"
	"testing"
)

func bucketWith(public bool, access BucketAccess) *Bucket {
	return &Bucket{Name: "files", Public: public, Access: access}
}

func TestBucketAccess_NoRuleRefusesEveryEndUser(t *testing.T) {
	bucket := bucketWith(false, nil)
	user := EndUser{Subject: "u1", Role: "authenticated"}
	for _, op := range []Operation{OpRead, OpWrite, OpDelete} {
		if bucket.Allows(user, op, "u1/a.txt") {
			t.Errorf("%v allowed with no rule", op)
		}
	}
}

func TestBucketAccess_OwnScopeIsTheCallersFolder(t *testing.T) {
	bucket := bucketWith(false, BucketAccess{"authenticated": {Read: ScopeOwn, Write: ScopeOwn, Delete: ScopeOwn}})
	user := EndUser{Subject: "u1", Role: "authenticated"}
	for _, op := range []Operation{OpRead, OpWrite, OpDelete} {
		if !bucket.Allows(user, op, "u1/photos/a.png") {
			t.Errorf("%v refused in the caller's own folder", op)
		}
		if bucket.Allows(user, op, "u2/a.png") {
			t.Errorf("%v allowed in another user's folder", op)
		}
		if bucket.Allows(user, op, "u1") {
			t.Errorf("%v allowed on the folder name itself", op)
		}
		if bucket.Allows(user, op, "u1x/a.png") {
			t.Errorf("%v allowed on a sibling whose name starts with the subject", op)
		}
	}
}

func TestBucketAccess_AllScopeCoversEveryKey(t *testing.T) {
	bucket := bucketWith(false, BucketAccess{"staff": {Write: ScopeAll}})
	staff := EndUser{Subject: "s1", Role: "staff"}
	if !bucket.Allows(staff, OpWrite, "products/lamp.png") {
		t.Error("all scope refused a key outside the caller's folder")
	}
	if bucket.Allows(staff, OpRead, "products/lamp.png") {
		t.Error("read was never granted")
	}
	if bucket.Allows(EndUser{Subject: "s1", Role: "authenticated"}, OpWrite, "products/lamp.png") {
		t.Error("a rule for one role applied to another")
	}
}

func TestBucketAccess_PublicBucketIsReadableByAnyone(t *testing.T) {
	bucket := bucketWith(true, nil)
	if !bucket.Allows(EndUser{Subject: "x", Role: "anon"}, OpRead, "logo.png") {
		t.Error("a public bucket's objects are readable from the internet already")
	}
	if bucket.Allows(EndUser{Subject: "x", Role: "anon"}, OpWrite, "logo.png") {
		t.Error("public is read-only")
	}
}

func TestBucketAccess_OwnScopeNeedsAUsableSubject(t *testing.T) {
	bucket := bucketWith(false, BucketAccess{"anon": {Read: ScopeOwn}})
	for _, subject := range []string{"", "a/b", ".", ".."} {
		if bucket.Allows(EndUser{Subject: subject, Role: "anon"}, OpRead, subject+"/a") {
			t.Errorf("subject %q got an own folder", subject)
		}
	}
}

func TestBucketAccess_ListPrefix(t *testing.T) {
	bucket := bucketWith(false, BucketAccess{
		"authenticated": {Read: ScopeOwn},
		"staff":         {Read: ScopeAll},
	})
	user := EndUser{Subject: "u1", Role: "authenticated"}
	cases := []struct {
		name   string
		user   EndUser
		prefix string
		want   string
		ok     bool
	}{
		{"own with no prefix lists the folder", user, "", "u1/", true},
		{"own inside the folder keeps the prefix", user, "u1/photos/", "u1/photos/", true},
		{"own outside the folder is refused", user, "u2/", "", false},
		{"all keeps the prefix", EndUser{Subject: "s", Role: "staff"}, "products/", "products/", true},
		{"no rule is refused", EndUser{Subject: "a", Role: "anon"}, "", "", false},
	}
	for _, tc := range cases {
		got, ok := bucket.ListPrefix(tc.user, tc.prefix)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestBucketAccess_PublicBucketStillNeedsARuleToList(t *testing.T) {
	if _, ok := bucketWith(true, nil).ListPrefix(EndUser{Subject: "x", Role: "anon"}, ""); ok {
		t.Error("public makes objects readable by key, not enumerable")
	}
}

func TestValidateBucketAccess(t *testing.T) {
	valid := BucketAccess{"authenticated": {Read: ScopeOwn, Write: ScopeOwn}, "staff_2": {Write: ScopeAll}}
	if err := ValidateBucketAccess(valid); err != nil {
		t.Fatalf("valid rules refused: %v", err)
	}
	bad := []BucketAccess{
		{"": {Read: ScopeOwn}},
		{"Admin": {Read: ScopeOwn}},
		{"a-b": {Read: ScopeOwn}},
		{"authenticated": {Read: "everyone"}},
		{"authenticated": {Delete: "mine"}},
	}
	for _, access := range bad {
		err := ValidateBucketAccess(access)
		var invalid *ValidationError
		if !errors.As(err, &invalid) {
			t.Errorf("%v: want a validation error, got %v", access, err)
		}
	}
	tooMany := BucketAccess{}
	for i := 0; i <= maxBucketAccessRoles; i++ {
		tooMany[string(rune('a'+i%26))+string(rune('a'+i/26))] = RoleAccess{Read: ScopeOwn}
	}
	if err := ValidateBucketAccess(tooMany); err == nil {
		t.Error("an unbounded rule list was accepted")
	}
}
