package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

type renewalLab struct {
	mock    *k8s.MockClient
	store   *storage.FileSystemStore
	minter  *recordingMinter
	renewer *BackupCredentialRenewer
	now     time.Time
}

func newRenewalLab(t *testing.T) *renewalLab {
	t.Helper()
	store, err := storage.NewFileSystemStore(t.TempDir())
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	lab := &renewalLab{mock: k8s.NewMockClient(), store: store, now: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)}
	lab.minter = &recordingMinter{now: func() time.Time { return lab.now }}
	lab.renewer = NewBackupCredentialRenewer(BackupCredentialRenewerConfig{
		Instances: store,
		Kube:      lab.mock,
		Storage:   StaticBackupStorage(platformStore()),
		Issuer:    newTestIssuer(t, lab.minter, newFakeObjectDeleter()),
		Now:       func() time.Time { return lab.now },
	})
	return lab
}

// project registers a backed-up project whose credential expires at expires.
func (lab *renewalLab) project(t *testing.T, id, status string, expires time.Time) {
	t.Helper()
	enabled := true
	if err := lab.store.Create(&domain.DatabaseInstance{
		ProjectID: id, OrgID: "org", Namespace: "org-" + id, Status: status,
		DBType: domain.PostgreSQL, DeploymentMode: domain.ModeK8s, BackupEnabled: &enabled,
	}); err != nil {
		t.Fatalf("create %s: %v", id, err)
	}
	if expires.IsZero() {
		return
	}
	data, err := k8s.BackupCredentialsSecretData(&domain.S3Credentials{
		AccessKeyID: "old", SecretAccessKey: "old", SessionToken: "old-" + id, ExpiresAt: expires,
		IssuedBy: k8s.BackupKeyFingerprint(platformStore().AccessKeyID),
	})
	if err != nil {
		t.Fatal(err)
	}
	lab.mock.Secrets["org-"+id+"/"+k8s.BackupCredentialsSecretName] = data
}

func (lab *renewalLab) token(id string) string {
	return string(lab.mock.Secrets["org-"+id+"/"+k8s.BackupCredentialsSecretName][k8s.BackupCredentialsSessionTokenKey])
}

func TestRenewalReplacesCredentialsPastHalfLifeAndLeavesFreshOnesAlone(t *testing.T) {
	lab := newRenewalLab(t)
	lab.project(t, "due", "ACTIVE", lab.now.Add(5*time.Hour))
	lab.project(t, "fresh", "ACTIVE", lab.now.Add(11*time.Hour))
	lab.project(t, "paused", string(domain.StatusPaused), lab.now.Add(time.Hour))
	lab.project(t, "expired", "ACTIVE", lab.now.Add(-time.Hour))

	report := lab.renewer.RenewDue(context.Background())
	if len(report.Failed) != 0 {
		t.Fatalf("failures: %v", report.Failed)
	}
	for _, id := range []string{"due", "paused", "expired"} {
		if got := lab.token(id); got != "token-"+id+"/cloud/" {
			t.Errorf("%s was not renewed: token %q", id, got)
		}
		data := lab.mock.Secrets["org-"+id+"/"+k8s.BackupCredentialsSecretName]
		if expiry, _ := k8s.BackupCredentialsExpiry(data); !expiry.Equal(lab.now.Add(12 * time.Hour)) {
			t.Errorf("%s renewed until %v, want a full lifetime", id, expiry)
		}
	}
	if got := lab.token("fresh"); got != "old-fresh" {
		t.Errorf("a credential with more than half its life left was replaced: %q", got)
	}
	if report.Renewed != 3 {
		t.Errorf("renewed %d, want 3", report.Renewed)
	}
}

func TestRenewalFollowsARotatedPlatformKeyAtOnce(t *testing.T) {
	// Revoking the old parent key kills every credential derived from it, so a
	// credential minted by another key is renewed however fresh it is.
	lab := newRenewalLab(t)
	lab.project(t, "fresh", "ACTIVE", lab.now.Add(11*time.Hour))
	data := lab.mock.Secrets["org-fresh/"+k8s.BackupCredentialsSecretName]
	data[k8s.BackupCredentialsIssuedByKey] = []byte(k8s.BackupKeyFingerprint("previous-platform-key"))

	if report := lab.renewer.RenewDue(context.Background()); report.Renewed != 1 {
		t.Fatalf("report %+v, want the credential from the old key renewed", report)
	}
	renewed := lab.mock.Secrets["org-fresh/"+k8s.BackupCredentialsSecretName]
	if string(renewed[k8s.BackupCredentialsIssuedByKey]) != k8s.BackupKeyFingerprint(platformStore().AccessKeyID) {
		t.Errorf("renewed credential names issuer %q", renewed[k8s.BackupCredentialsIssuedByKey])
	}
	if report := lab.renewer.RenewDue(context.Background()); report.Renewed != 0 {
		t.Errorf("a credential from the current key was renewed again: %+v", report)
	}
}

func TestRenewalSkipsWhatDoesNotArchive(t *testing.T) {
	lab := newRenewalLab(t)
	lab.project(t, "deleting", string(domain.StatusDeleting), lab.now.Add(time.Hour))
	lab.project(t, "creating", "PROVISIONING", time.Time{})
	disabled := false
	_ = lab.store.Create(&domain.DatabaseInstance{ProjectID: "nobackup", OrgID: "org", Namespace: "org-nobackup", Status: "ACTIVE", DeploymentMode: domain.ModeK8s, BackupEnabled: &disabled})
	enabled := true
	_ = lab.store.Create(&domain.DatabaseInstance{ProjectID: "docker", OrgID: "org", Namespace: "org-docker", Status: "ACTIVE", DeploymentMode: domain.ModeDocker, BackupEnabled: &enabled})

	report := lab.renewer.RenewDue(context.Background())
	if len(report.Failed) != 0 || report.Renewed != 0 {
		t.Fatalf("report %+v, want nothing touched", report)
	}
	for _, call := range lab.mock.Calls {
		if strings.HasPrefix(call, "UpdateSecret:") {
			t.Errorf("unexpected %s", call)
		}
	}
}

func TestAFailedRenewalIsReportedAndTheOthersStillRenew(t *testing.T) {
	lab := newRenewalLab(t)
	lab.project(t, "a", "ACTIVE", lab.now.Add(time.Hour))
	lab.project(t, "b", "ACTIVE", lab.now.Add(time.Hour))
	lab.project(t, "lost", "ACTIVE", time.Time{}) // archives, but its secret is gone

	report := lab.renewer.RenewDue(context.Background())
	if report.Renewed != 2 {
		t.Errorf("renewed %d, want 2", report.Renewed)
	}
	if len(report.Failed) != 1 || !strings.Contains(report.Failed[0].Error(), "lost") {
		t.Fatalf("failures %v, want the project whose secret is missing", report.Failed)
	}
}

func TestRenewalFailsLoudlyWhenMintingFails(t *testing.T) {
	lab := newRenewalLab(t)
	lab.project(t, "a", "ACTIVE", lab.now.Add(time.Hour))
	lab.minter.err = errors.New("signer down")

	report := lab.renewer.RenewDue(context.Background())
	if len(report.Failed) != 1 || report.Renewed != 0 {
		t.Fatalf("report %+v, want one failure", report)
	}
	if got := lab.token("a"); got != "old-a" {
		t.Errorf("a failed renewal must leave the current credential in place, got %q", got)
	}
}

func TestRenewalFailsForEveryProjectWhenTheStoreIsGone(t *testing.T) {
	lab := newRenewalLab(t)
	lab.project(t, "a", "ACTIVE", lab.now.Add(time.Hour))
	lab.renewer.cfg.Storage = StaticBackupStorage(nil)

	if report := lab.renewer.RenewDue(context.Background()); len(report.Failed) != 1 {
		t.Fatalf("report %+v, want a failure", report)
	}
}

type fixedLeader struct{ leader bool }

func (f fixedLeader) IsLeader(context.Context) (bool, error) { return f.leader, nil }

func TestOnlyTheLeaderRenews(t *testing.T) {
	lab := newRenewalLab(t)
	lab.project(t, "a", "ACTIVE", lab.now.Add(time.Hour))

	lab.renewer.renewIfLeader(context.Background(), fixedLeader{leader: false})
	if got := lab.token("a"); got != "old-a" {
		t.Fatalf("a follower renewed: %q", got)
	}
	lab.renewer.renewIfLeader(context.Background(), fixedLeader{leader: true})
	if got := lab.token("a"); got == "old-a" {
		t.Fatal("the leader did not renew")
	}
}
