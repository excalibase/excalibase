package abuse

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// AuditActionProxyRefused is the audit action written when an app is refused as a proxy.
const AuditActionProxyRefused = "app.refused.proxy"

// ErrProxyApp is what a refused app's error matches; handlers answer it 403.
var ErrProxyApp = errors.New("this app looks like a proxy or VPN node")

// ProxyRefusal is the refusal with what matched, for the caller's message.
type ProxyRefusal struct{ Finding Finding }

func (e *ProxyRefusal) Error() string {
	return ErrProxyApp.Error() + " (" + e.Finding.Reason() + "). Running proxies, VPNs or traffic relays is not allowed; " +
		"if this is a mistake, contact support"
}

func (e *ProxyRefusal) Unwrap() error { return ErrProxyApp }

// AuditWriter is the audit sink a refusal is recorded in.
type AuditWriter interface {
	LogAudit(ctx context.Context, entry *domain.AuditEntry) error
}

// AppSubject is what is known of an app at the point it is created, changed or deployed.
type AppSubject struct {
	ProjectID, AppID, Name, Image string
	// EnvNames are variable names only; values are never read.
	EnvNames []string
	Actor    string
	// Surface names where the request came in: create, update, secret, deploy or template.
	Surface string
}

// AppGuard refuses proxy apps while features.ProxyBlock is on.
type AppGuard struct {
	flags features.Flags
	audit AuditWriter
}

// NewAppGuard: a nil audit still refuses, it only records nothing.
func NewAppGuard(flags features.Flags, audit AuditWriter) *AppGuard {
	return &AppGuard{flags: flags, audit: audit}
}

// Check returns a *ProxyRefusal for a proxy app and nil otherwise. A nil guard allows everything.
func (g *AppGuard) Check(ctx context.Context, subject AppSubject) error {
	if g == nil || !features.Enabled(ctx, g.flags, features.ProxyBlock) {
		return nil
	}
	finding := FindProxy(subject.Image, subject.Name, subject.EnvNames)
	if finding == nil {
		return nil
	}
	g.record(ctx, subject, *finding)
	return &ProxyRefusal{Finding: *finding}
}

func (g *AppGuard) record(ctx context.Context, subject AppSubject, finding Finding) {
	if g.audit == nil {
		return
	}
	now := time.Now().UTC()
	details := fmt.Sprintf("surface=%s name=%q image=%q %s", subject.Surface, subject.Name, subject.Image, finding.Reason())
	entry := &domain.AuditEntry{
		UserID: subject.Actor, Action: AuditActionProxyRefused, Resource: "app", ResourceID: subject.AppID,
		ProjectID: subject.ProjectID, Details: strings.TrimSpace(details), Timestamp: &now,
	}
	if err := g.audit.LogAudit(ctx, entry); err != nil {
		log.Printf("ERROR: audit of a refused proxy app in project %s: %v", subject.ProjectID, err)
	}
}
