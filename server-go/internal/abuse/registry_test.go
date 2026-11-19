package abuse

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

func TestBlockedRegistry_MatchesTheRegistryHostOnly(t *testing.T) {
	blocked := []string{
		"ttl.sh/foo:1h", "ttl.sh/foo", "TTL.SH/Foo:1h", "ttl.sh:443/foo:1h", "ttl.sh./foo",
		"ttl.sh/a/b/c:2h", "ttl.sh/foo@sha256:" + strings.Repeat("a", 64), "ttl.sh/foo:1h@sha256:" + strings.Repeat("b", 64),
		"  ttl.sh/foo:1h ",
		"eu.ttl.sh/x:1h", "x.ttl.sh/foo", "a.b.TTL.SH:443/foo:1h", "eu.ttl.sh./foo",
	}
	for _, image := range blocked {
		if !BlockedRegistry(image) {
			t.Errorf("%q passed, want blocked", image)
		}
	}
	allowed := []string{
		"nginx", "nginx:1.27", "library/nginx", "ghcr.io/acme/app:1", "docker.io/library/nginx", "docker.io/acme/app@sha256:" + strings.Repeat("c", 64),
		"registry.example.com:5000/x", "localhost:5000/x", "docker.io/ttl.sh-fan/foo", "ghcr.io/x/ttl.sh", "ghcr.io/x/ttl.sh:1h",
		"ttl.sh-fan/foo", "notttl.sh/foo", "ttl.sh.example.com/foo", "ttl.shop.example/foo:1", "ttl/sh", "",
	}
	for _, image := range allowed {
		if BlockedRegistry(image) {
			t.Errorf("%q blocked, want allowed", image)
		}
	}
}

func TestAppGuard_RefusesABlockedRegistryEvenWithTheProxyFlagOff(t *testing.T) {
	audit := &recordedAudit{}
	guard := NewAppGuard(features.NewStatic(), audit)

	err := guard.Check(context.Background(), AppSubject{ProjectID: "p1", AppID: "a1", Name: "web", Image: "ttl.sh/abc:1h", Surface: "create"})

	if !errors.Is(err, ErrBlockedRegistry) {
		t.Fatalf("err = %v, want ErrBlockedRegistry", err)
	}
	want := "images from ttl.sh are not allowed; push to a registry such as Docker Hub or GHCR"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err, want)
	}
	if len(audit.entries) != 1 || audit.entries[0].Action != AuditActionRegistryRefused {
		t.Errorf("audit entries = %+v", audit.entries)
	}
	if err := guard.Check(context.Background(), AppSubject{Name: "web", Image: "ghcr.io/acme/web:1"}); err != nil {
		t.Errorf("normal image refused: %v", err)
	}
}
