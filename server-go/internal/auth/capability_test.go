package auth

import (
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

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
		{name: "surrounding space is trimmed", raw: "  policies:read  ", resource: "policies", action: "read"},
		{name: "empty", raw: "", wantErr: true},
		{name: "resource only", raw: "vault", wantErr: true},
		{name: "empty action", raw: "vault:", wantErr: true},
		{name: "empty resource", raw: ":read", wantErr: true},
		{name: "too many parts", raw: "vault:read:a:b", wantErr: true},
		{name: "uppercase resource", raw: "Vault:read", wantErr: true},
		{name: "wildcard resource", raw: "*:read", wantErr: true},
		{name: "wildcard action", raw: "vault:*", wantErr: true},
		{name: "interior wildcard", raw: "vault:read:pki/*/private", wantErr: true},
		{name: "multiple wildcards", raw: "vault:read:pki/**", wantErr: true},
		{name: "parent traversal selector", raw: "vault:read:pki/../secrets", wantErr: true},
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			granted, err := ParseCapability(tc.granted)
			if err != nil {
				t.Fatalf("granted %q: %v", tc.granted, err)
			}
			want, err := ParseCapability(tc.want)
			if err != nil {
				t.Fatalf("want %q: %v", tc.want, err)
			}
			if got := granted.Grants(want); got != tc.allow {
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
