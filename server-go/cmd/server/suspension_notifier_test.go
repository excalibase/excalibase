package main

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/service"
)

type recordingSender struct{ bodies []string }

func (r *recordingSender) Send(_ context.Context, msg email.Message) (string, error) {
	r.bodies = append(r.bodies, msg.TextBody)
	return "id", nil
}

func closureBodyFor(t *testing.T, backups service.BackupStorageSource) string {
	t.Helper()
	sender := &recordingSender{}
	cfg := config.AppConfig{BackupBucket: "excalibase-backups", EmailFromAddress: "noreply@example.test"}
	notifier := suspensionNotifier(cfg, sender, backups)
	if err := notifier.Suspended(context.Background(), adminconsole.SuspensionNotice{Email: "u@example.test", OrgClosed: true}); err != nil {
		t.Fatal(err)
	}
	return sender.bodies[0]
}

// BACKUP_DEFAULT_BUCKET always has a value, so the bucket name says nothing:
// the closure mail follows whether backup storage is really configured.
func TestSuspensionNotifier_ClosureMailFollowsTheBackupStorage(t *testing.T) {
	none := closureBodyFor(t, service.StaticBackupStorage(nil))
	if strings.Contains(none, "14 days") || !strings.Contains(none, "No backups of the databases exist") {
		t.Errorf("no backup storage, yet the mail promises backups:\n%s", none)
	}
	some := closureBodyFor(t, service.StaticBackupStorage(&domain.S3Credentials{Bucket: "b", Endpoint: "https://r2"}))
	if !strings.Contains(some, "kept for 14 days") {
		t.Errorf("backup storage is configured, yet the mail does not mention it:\n%s", some)
	}
}
