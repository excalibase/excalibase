package auth

import (
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// The selectors the platform mints its own services with (mirrored in
// charts/platform-aio values.yaml).
const (
	capAuthCredentials   = "vault:read:projects/*/credentials/auth_admin"
	capEngineCredentials = "vault:read:projects/*/credentials/excalibase_app"
)

// rawCapability builds a wanted capability without validating it, the way the
// request gate does: the subject comes from a URL, so Grants must refuse a
// malformed one rather than rely on it having been parsed.
func rawCapability(raw string) Capability {
	parts := strings.SplitN(raw, ":", 3)
	want := Capability{Resource: parts[0], Action: parts[1]}
	if len(parts) == 3 {
		want.Selector = parts[2]
	}
	return want
}

func TestParseCapability(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantErr  bool
		resource string
		action   string
		selector string
	}{
		{name: "resource and action", raw: "policies:read", resource: "policies", action: "read"},
		{name: "with selector", raw: "projects:info:read", resource: "projects", action: "info", selector: "read"},
		{name: "path selector", raw: "vault:read:pki/signing/private", resource: "vault", action: "read", selector: "pki/signing/private"},
		{name: "trailing wildcard selector", raw: "vault:read:pki/signing/*", resource: "vault", action: "read", selector: "pki/signing/*"},
		{name: "interior wildcard segment", raw: "vault:read:projects/*/credentials/auth_admin",
			resource: "vault", action: "read", selector: "projects/*/credentials/auth_admin"},
		{name: "leading wildcard segment", raw: "vault:read:*/credentials/excalibase_app",
			resource: "vault", action: "read", selector: "*/credentials/excalibase_app"},
		{name: "two wildcard segments", raw: "vault:read:projects/*/jwt_keys/*",
			resource: "vault", action: "read", selector: "projects/*/jwt_keys/*"},
		{name: "surrounding space is trimmed", raw: "  policies:read  ", resource: "policies", action: "read"},
		{name: "empty", raw: "", wantErr: true},
		{name: "resource only", raw: "vault", wantErr: true},
		{name: "empty action", raw: "vault:", wantErr: true},
		{name: "empty resource", raw: ":read", wantErr: true},
		{name: "too many parts", raw: "vault:read:a:b", wantErr: true},
		{name: "uppercase resource", raw: "Vault:read", wantErr: true},
		{name: "wildcard resource", raw: "*:read", wantErr: true},
		{name: "wildcard action", raw: "vault:*", wantErr: true},
		{name: "multiple wildcards", raw: "vault:read:pki/**", wantErr: true},
		{name: "parent traversal selector", raw: "vault:read:pki/../secrets", wantErr: true},
		{name: "wildcard suffix within a segment", raw: "vault:read:projects/p/credentials/auth_*", wantErr: true},
		{name: "wildcard prefix within a segment", raw: "vault:read:projects/p/credentials/*admin", wantErr: true},
		{name: "wildcard inside a segment", raw: "vault:read:projects/p*1/credentials/admin", wantErr: true},
		{name: "bare wildcard selector", raw: "vault:read:*", wantErr: true},
		{name: "all segments wildcard", raw: "vault:read:*/*", wantErr: true},
		{name: "empty interior segment", raw: "vault:read:projects//credentials", wantErr: true},
		{name: "leading slash selector", raw: "vault:read:/projects/p/credentials", wantErr: true},
		{name: "trailing slash selector", raw: "vault:read:projects/p/credentials/", wantErr: true},
		{name: "current directory segment", raw: "vault:read:projects/./credentials", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCapability(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseCapability(%q) = %+v, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseCapability(%q): %v", tc.raw, err)
			}
			if got.Resource != tc.resource || got.Action != tc.action || got.Selector != tc.selector {
				t.Fatalf("ParseCapability(%q) = %+v, want {%s %s %s}", tc.raw, got, tc.resource, tc.action, tc.selector)
			}
		})
	}
}

func TestCapabilityString(t *testing.T) {
	if got := (Capability{Resource: "policies", Action: "read"}).String(); got != "policies:read" {
		t.Fatalf("String() = %q", got)
	}
	withSelector := Capability{Resource: "vault", Action: "read", Selector: "pki/signing/private"}
	if got := withSelector.String(); got != "vault:read:pki/signing/private" {
		t.Fatalf("String() = %q", got)
	}
}

func TestCapabilityGrants(t *testing.T) {
	cases := []struct {
		name    string
		granted string
		want    string
		allow   bool
	}{
		{name: "exact match", granted: "policies:read", want: "policies:read", allow: true},
		{name: "selector exact", granted: "projects:info:read", want: "projects:info:read", allow: true},
		{name: "wildcard covers leaf", granted: "vault:read:pki/signing/*", want: "vault:read:pki/signing/private", allow: true},
		{name: "wildcard covers other leaf", granted: "vault:read:pki/signing/*", want: "vault:read:pki/signing/public", allow: true},
		{name: "wildcard does not cross a slash", granted: "vault:read:pki/signing/*", want: "vault:read:pki/signing/sub/key", allow: false},
		{name: "wildcard needs a leaf", granted: "vault:read:pki/signing/*", want: "vault:read:pki/signing/", allow: false},
		{name: "different subtree", granted: "vault:read:pki/signing/*", want: "vault:read:pki/other", allow: false},
		{name: "prefix is not a subtree", granted: "vault:read:pki/sign/*", want: "vault:read:pki/signing/private", allow: false},
		{name: "different resource", granted: "vault:read:pki/signing/*", want: "policies:read", allow: false},
		{name: "different action", granted: "vault:read:pki/signing/*", want: "vault:write:pki/signing/private", allow: false},
		{name: "granted without selector does not cover one", granted: "vault:read", want: "vault:read:pki/signing/private", allow: false},
		{name: "granted with selector does not cover bare", granted: "vault:read:pki/signing/*", want: "vault:read", allow: false},

		// The two selectors the platform's own services are minted with: each
		// service reads one role's credentials, in any project, and nothing else.
		{name: "auth reads its role in one project", granted: capAuthCredentials,
			want: "vault:read:projects/proj-1/credentials/auth_admin", allow: true},
		{name: "auth reads its role in another project", granted: capAuthCredentials,
			want: "vault:read:projects/tenant-b/credentials/auth_admin", allow: true},
		{name: "auth may not read the engine role", granted: capAuthCredentials,
			want: "vault:read:projects/proj-1/credentials/excalibase_app", allow: false},
		{name: "auth may not read the owner role", granted: capAuthCredentials,
			want: "vault:read:projects/proj-1/credentials/admin", allow: false},
		{name: "auth may not read the watcher role", granted: capAuthCredentials,
			want: "vault:read:projects/proj-1/credentials/cdc_watcher", allow: false},
		{name: "auth may not read deeper", granted: capAuthCredentials,
			want: "vault:read:projects/proj-1/credentials/auth_admin/password", allow: false},
		{name: "auth may not read shallower", granted: capAuthCredentials,
			want: "vault:read:projects/proj-1/credentials", allow: false},
		{name: "auth may not read the signing keys through it", granted: capAuthCredentials,
			want: "vault:read:pki/signing/private", allow: false},
		{name: "wildcard does not stand for an empty project id", granted: capAuthCredentials,
			want: "vault:read:projects//credentials/auth_admin", allow: false},
		{name: "wildcard does not stand for a traversal", granted: capAuthCredentials,
			want: "vault:read:projects/../credentials/auth_admin", allow: false},
		{name: "wildcard does not stand for a current directory", granted: capAuthCredentials,
			want: "vault:read:projects/./credentials/auth_admin", allow: false},
		{name: "wildcard does not span two segments", granted: capAuthCredentials,
			want: "vault:read:projects/a/b/credentials/auth_admin", allow: false},
		{name: "engine reads its own role", granted: capEngineCredentials,
			want: "vault:read:projects/proj-1/credentials/excalibase_app", allow: true},
		{name: "engine may not read the auth role", granted: capEngineCredentials,
			want: "vault:read:projects/proj-1/credentials/auth_admin", allow: false},
		{name: "engine may not read the owner role", granted: capEngineCredentials,
			want: "vault:read:projects/proj-1/credentials/admin", allow: false},
		{name: "two wildcards each stand for one segment", granted: "vault:read:projects/*/jwt_keys/*",
			want: "vault:read:projects/proj-1/jwt_keys/anon_token", allow: true},
		{name: "two wildcards do not stretch", granted: "vault:read:projects/*/jwt_keys/*",
			want: "vault:read:projects/proj-1/jwt_keys/anon/token", allow: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			granted, err := ParseCapability(tc.granted)
			if err != nil {
				t.Fatalf("granted %q: %v", tc.granted, err)
			}
			if got := granted.Grants(rawCapability(tc.want)); got != tc.allow {
				t.Fatalf("%q grants %q = %v, want %v", tc.granted, tc.want, got, tc.allow)
			}
		})
	}
}

func TestNormalizeCapabilities(t *testing.T) {
	got, err := NormalizeCapabilities([]string{" policies:read ", "vault:read:pki/signing/*", "policies:read"})
	if err != nil {
		t.Fatalf("NormalizeCapabilities: %v", err)
	}
	want := []string{"policies:read", "vault:read:pki/signing/*"}
	if len(got) != len(want) {
		t.Fatalf("NormalizeCapabilities = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("NormalizeCapabilities = %v, want %v", got, want)
		}
	}
	if empty, err := NormalizeCapabilities(nil); err != nil || len(empty) != 0 {
		t.Fatalf("NormalizeCapabilities(nil) = %v, %v", empty, err)
	}
	if _, err := NormalizeCapabilities([]string{"nope"}); err == nil {
		t.Fatal("NormalizeCapabilities accepted a malformed entry")
	}
	if _, err := NormalizeCapabilities([]string{capAuthCredentials, capEngineCredentials}); err != nil {
		t.Fatalf("NormalizeCapabilities rejected a minted service permission: %v", err)
	}
}

func TestIsCapabilityToken(t *testing.T) {
	if IsCapabilityToken(nil) {
		t.Fatal("nil token must not be a capability token")
	}
	if IsCapabilityToken(&domain.AccessToken{}) {
		t.Fatal("token without permissions must not be a capability token")
	}
	if !IsCapabilityToken(&domain.AccessToken{Permissions: []string{"policies:read"}}) {
		t.Fatal("token with permissions must be a capability token")
	}
}

func TestTokenGrants(t *testing.T) {
	policies := Capability{Resource: "policies", Action: "read"}
	signing := Capability{Resource: "vault", Action: "read", Selector: "pki/signing/private"}

	scoped := &domain.AccessToken{Permissions: []string{"policies:read", "projects:info:read"}}
	if !TokenGrants(scoped, policies) {
		t.Fatal("token should grant policies:read")
	}
	if TokenGrants(scoped, signing) {
		t.Fatal("token must not grant a vault read it does not list")
	}
	if TokenGrants(nil, policies) {
		t.Fatal("nil token grants nothing")
	}
	// A token with no permissions is a human PAT: the capability layer is not
	// its gate, so it grants nothing here and is skipped by the gate instead.
	if TokenGrants(&domain.AccessToken{}, policies) {
		t.Fatal("non-capability token must not be granted through this path")
	}
	// A malformed stored permission is ignored rather than crashing the gate.
	broken := &domain.AccessToken{Permissions: []string{"garbage", "policies:read"}}
	if !TokenGrants(broken, policies) {
		t.Fatal("a malformed entry must not void the valid ones")
	}
}

func TestTokenGrantsSelfImplicitly(t *testing.T) {
	// Every capability token may validate its own credential, whatever its
	// permission list says — a service must be able to reach /api/auth/me.
	narrow := &domain.AccessToken{Permissions: []string{"policies:read"}}
	if !TokenGrants(narrow, SelfCapability()) {
		t.Fatal("a capability token must always grant self:read")
	}
	// The implicit grant does not extend to a non-capability token: the
	// gate leaves human PATs alone rather than granting them anything here.
	if TokenGrants(&domain.AccessToken{}, SelfCapability()) {
		t.Fatal("a permission-less token must not be granted through this path")
	}
	// The grant is exactly self:read; a token cannot reach self:write by it.
	if TokenGrants(narrow, Capability{Resource: "self", Action: "write"}) {
		t.Fatal("only self:read is implicit")
	}
}
