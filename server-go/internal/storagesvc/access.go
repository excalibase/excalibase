package storagesvc

import (
	"regexp"
	"strings"
)

// Scope is how much of a bucket a role's rule reaches.
type Scope string

const (
	// ScopeNone grants nothing; it is the zero value, so an absent field denies.
	ScopeNone Scope = ""
	// ScopeOwn reaches the keys under the caller's own folder, "<sub>/".
	ScopeOwn Scope = "own"
	// ScopeAll reaches every key in the bucket.
	ScopeAll Scope = "all"
)

// RoleAccess is what one end-user role may do in a bucket.
type RoleAccess struct {
	Read   Scope `json:"read,omitempty"`
	Write  Scope `json:"write,omitempty"`
	Delete Scope `json:"delete,omitempty"`
}

// BucketAccess maps an end-user role (the token's role claim) to its rule. A
// role with no entry gets nothing, as in Hasura: app users reach a bucket only
// through a rule the project wrote.
type BucketAccess map[string]RoleAccess

// Operation is one kind of end-user request against a bucket.
type Operation int

const (
	OpRead Operation = iota
	OpWrite
	OpDelete
)

// EndUser is a verified app user: the subject and role of a project token.
type EndUser struct {
	Subject string
	Role    string
}

const maxBucketAccessRoles = 32

var roleNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// ValidateBucketAccess refuses rules that name an impossible role or scope.
func ValidateBucketAccess(access BucketAccess) error {
	if len(access) > maxBucketAccessRoles {
		return invalidf("at most %d roles may have access rules", maxBucketAccessRoles)
	}
	for role, rule := range access {
		if !roleNamePattern.MatchString(role) {
			return invalidf("access role %q must be lowercase letters, digits and underscores", role)
		}
		for _, scope := range []Scope{rule.Read, rule.Write, rule.Delete} {
			if scope != ScopeNone && scope != ScopeOwn && scope != ScopeAll {
				return invalidf("access scope %q for role %q must be %q or %q", scope, role, ScopeOwn, ScopeAll)
			}
		}
	}
	return nil
}

func (r RoleAccess) scope(op Operation) Scope {
	switch op {
	case OpRead:
		return r.Read
	case OpWrite:
		return r.Write
	default:
		return r.Delete
	}
}

// Allows reports whether the user may perform op on key. A public bucket is
// readable by anyone, since its objects are served to the internet already.
func (b *Bucket) Allows(user EndUser, op Operation, key string) bool {
	if op == OpRead && b.Public {
		return true
	}
	switch b.Access[user.Role].scope(op) {
	case ScopeAll:
		return true
	case ScopeOwn:
		folder, ok := ownFolder(user)
		return ok && strings.HasPrefix(key, folder) && len(key) > len(folder)
	default:
		return false
	}
}

// ListPrefix is the prefix a listing by user runs under: the requested one
// when the role reads everything, or one inside the caller's own folder. ok is
// false when the user may not list at all, or asked outside their folder.
// Public alone does not make a bucket enumerable.
func (b *Bucket) ListPrefix(user EndUser, requested string) (string, bool) {
	switch b.Access[user.Role].Read {
	case ScopeAll:
		return requested, true
	case ScopeOwn:
		folder, ok := ownFolder(user)
		if !ok {
			return "", false
		}
		if requested == "" {
			return folder, true
		}
		if !strings.HasPrefix(requested, folder) {
			return "", false
		}
		return requested, true
	default:
		return "", false
	}
}

// ownFolder is "<sub>/", for a subject that is one usable path segment.
func ownFolder(user EndUser) (string, bool) {
	sub := user.Subject
	if sub == "" || sub == "." || sub == ".." || strings.ContainsAny(sub, "/\\") {
		return "", false
	}
	return sub + "/", true
}
