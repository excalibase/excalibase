package abuse

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

type recordedAudit struct{ entries []*domain.AuditEntry }

func (r *recordedAudit) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	r.entries = append(r.entries, entry)
	return nil
}

var proxyApp = AppSubject{
	ProjectID: "p1", AppID: "a1", Name: "web", Image: "ghcr.io/xtls/xray-core:1",
	EnvNames: []string{"PORT"}, Actor: "u1", Surface: "deploy",
}

func TestAppGuard_RefusesAProxyAndWritesAnAuditEvent(t *testing.T) {
	audit := &recordedAudit{}
	guard := NewAppGuard(features.NewStatic(features.ProxyBlock), audit)

	err := guard.Check(context.Background(), proxyApp)

	if !errors.Is(err, ErrProxyApp) {
		t.Fatalf("err = %v, want ErrProxyApp", err)
	}
	if !strings.Contains(err.Error(), "proxy") {
		t.Errorf("message does not say why: %q", err)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("%d audit entries, want 1", len(audit.entries))
	}
	entry := audit.entries[0]
	if entry.Action != AuditActionProxyRefused || entry.ProjectID != "p1" || entry.UserID != "u1" || entry.ResourceID != "a1" {
		t.Errorf("entry = %+v", entry)
	}
	if !strings.Contains(entry.Details, "deploy") || !strings.Contains(entry.Details, "xray") {
		t.Errorf("details lack surface or signature: %q", entry.Details)
	}
}

func TestAppGuard_AllowsANormalApp(t *testing.T) {
	audit := &recordedAudit{}
	guard := NewAppGuard(features.NewStatic(features.ProxyBlock), audit)
	normal := AppSubject{ProjectID: "p1", Name: "api", Image: "node:22", EnvNames: []string{"DATABASE_URL", "PORT"}}

	if err := guard.Check(context.Background(), normal); err != nil {
		t.Fatalf("normal app refused: %v", err)
	}
	if len(audit.entries) != 0 {
		t.Errorf("audit entries for an allowed app: %+v", audit.entries)
	}
}

func TestAppGuard_RefusesByEnvironmentAndNeverLogsValues(t *testing.T) {
	audit := &recordedAudit{}
	guard := NewAppGuard(features.NewStatic(features.ProxyBlock), audit)
	subject := AppSubject{ProjectID: "p1", Name: "api", Image: "node:22", EnvNames: []string{"UUID", "WS_PATH"}, Surface: "update"}

	if err := guard.Check(context.Background(), subject); !errors.Is(err, ErrProxyApp) {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(audit.entries[0].Details, "UUID") {
		t.Errorf("details lack the key names: %q", audit.entries[0].Details)
	}
}

func TestAppGuard_OffFlagAndNilGuardAllow(t *testing.T) {
	off := NewAppGuard(features.NewStatic(), &recordedAudit{})
	if err := off.Check(context.Background(), proxyApp); err != nil {
		t.Errorf("flag off: %v", err)
	}
	var none *AppGuard
	if err := none.Check(context.Background(), proxyApp); err != nil {
		t.Errorf("nil guard: %v", err)
	}
}

type failingAudit struct{}

func (failingAudit) LogAudit(context.Context, *domain.AuditEntry) error { return errors.New("db down") }

func TestAppGuard_StillRefusesWhenTheAuditWriteFails(t *testing.T) {
	guard := NewAppGuard(features.NewStatic(features.ProxyBlock), failingAudit{})
	if err := guard.Check(context.Background(), proxyApp); !errors.Is(err, ErrProxyApp) {
		t.Fatalf("err = %v", err)
	}
}
