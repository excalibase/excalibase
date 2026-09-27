package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/objectcreds"
)

// recordingMinter answers every mint with a credential that names its scope,
// so a test can see exactly what a tenant was handed.
type recordingMinter struct {
	mu      sync.Mutex
	scopes  []objectcreds.Scope
	parents []objectcreds.Parent
	err     error
	now     func() time.Time
}

func (m *recordingMinter) Mint(_ context.Context, parent objectcreds.Parent, scope objectcreds.Scope) (objectcreds.Credentials, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return objectcreds.Credentials{}, m.err
	}
	m.scopes = append(m.scopes, scope)
	m.parents = append(m.parents, parent)
	now := time.Now
	if m.now != nil {
		now = m.now
	}
	return objectcreds.Credentials{
		AccessKeyID:     "tmp-" + scope.Prefix,
		SecretAccessKey: "tmp-secret-" + string(scope.Access),
		SessionToken:    "token-" + scope.Prefix,
		ExpiresAt:       now().Add(scope.TTL),
	}, nil
}

func (m *recordingMinter) minted() []objectcreds.Scope {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]objectcreds.Scope(nil), m.scopes...)
}

func platformStore() *domain.S3Credentials {
	return &domain.S3Credentials{AccessKeyID: "platform-key", SecretAccessKey: "platform-secret", Endpoint: testR2Endpoint, Bucket: "backups", Region: "auto"}
}

func newTestIssuer(t *testing.T, minter objectcreds.Minter, objects ObjectDeleter) *BackupCredentialIssuer {
	t.Helper()
	issuer, err := NewBackupCredentialIssuer(BackupCredentialIssuerConfig{
		Minter:    minter,
		OpenStore: func(context.Context, *domain.S3Credentials) (ObjectDeleter, error) { return objects, nil },
		TTL:       12 * time.Hour,
		SourceTTL: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewBackupCredentialIssuer: %v", err)
	}
	return issuer
}

func TestANewProjectGetsReadWriteCredentialsForItsOwnPrefixOnly(t *testing.T) {
	minter := &recordingMinter{}
	issuer := newTestIssuer(t, minter, newFakeObjectDeleter("other/cloud/wals/1"))

	creds, err := issuer.ForNewProject(context.Background(), platformStore(), "proj1")
	if err != nil {
		t.Fatalf("ForNewProject: %v", err)
	}
	scopes := minter.minted()
	if len(scopes) != 1 || scopes[0] != (objectcreds.Scope{Prefix: "proj1/cloud/", Access: objectcreds.ReadWrite, TTL: 12 * time.Hour}) {
		t.Fatalf("minted %+v", scopes)
	}
	if creds.AccessKeyID == "platform-key" || creds.SecretAccessKey == "platform-secret" || creds.SessionToken == "" {
		t.Fatalf("the platform key must never be handed out: %+v", creds)
	}
	if creds.Bucket != "backups" || creds.Endpoint != testR2Endpoint || creds.Region != "auto" {
		t.Errorf("the credential must still name where it works: %+v", creds)
	}
	if creds.IssuedBy != k8s.BackupKeyFingerprint("platform-key") {
		t.Errorf("issued by %q, want the platform key's fingerprint", creds.IssuedBy)
	}
	if minter.parents[0].AccessKeyID != "platform-key" || minter.parents[0].Bucket != "backups" {
		t.Errorf("minted from %+v, want the platform store", minter.parents[0])
	}
}

func TestANewProjectIsRefusedAPrefixThatAlreadyHoldsBackups(t *testing.T) {
	// A retained prefix of a deleted project must never be handed to whoever
	// gets its id next: the new holder could read or delete it.
	minter := &recordingMinter{}
	issuer := newTestIssuer(t, minter, newFakeObjectDeleter("proj1/cloud/base/20260901/data.tar"))

	if _, err := issuer.ForNewProject(context.Background(), platformStore(), "proj1"); !errors.Is(err, ErrBackupPrefixInUse) {
		t.Fatalf("err = %v, want ErrBackupPrefixInUse", err)
	}
	if len(minter.minted()) != 0 {
		t.Error("nothing may be minted for a prefix that is in use")
	}
}

func TestAPrefixThatCannotBeCheckedIsNotHandedOut(t *testing.T) {
	objects := newFakeObjectDeleter()
	objects.listErr = errors.New("store down")
	minter := &recordingMinter{}
	if _, err := newTestIssuer(t, minter, objects).ForNewProject(context.Background(), platformStore(), "proj1"); err == nil {
		t.Fatal("an unverifiable prefix must refuse the project")
	}
	if len(minter.minted()) != 0 {
		t.Error("nothing may be minted without the check")
	}
}

func TestRenewalMintsAgainWithoutTheEmptinessCheck(t *testing.T) {
	minter := &recordingMinter{}
	objects := newFakeObjectDeleter("proj1/cloud/wals/000000010000000000000001")
	issuer := newTestIssuer(t, minter, objects)

	if _, err := issuer.Renew(context.Background(), platformStore(), "proj1"); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if scopes := minter.minted(); len(scopes) != 1 || scopes[0].Prefix != "proj1/cloud/" || scopes[0].Access != objectcreds.ReadWrite {
		t.Fatalf("minted %+v", scopes)
	}
	if objects.listCalls != 0 {
		t.Error("renewal of a running project's own prefix must not require it to be empty")
	}
}

func TestARestoreReadsItsSourceReadOnlyForAShortTime(t *testing.T) {
	minter := &recordingMinter{}
	creds, err := newTestIssuer(t, minter, newFakeObjectDeleter()).ForRestoreSource(context.Background(), platformStore(), "src")
	if err != nil {
		t.Fatalf("ForRestoreSource: %v", err)
	}
	if scopes := minter.minted(); len(scopes) != 1 || scopes[0] != (objectcreds.Scope{Prefix: "src/cloud/", Access: objectcreds.ReadOnly, TTL: 2 * time.Hour}) {
		t.Fatalf("minted %+v", scopes)
	}
	if creds.SessionToken == "" {
		t.Error("restore source credentials must be temporary")
	}
}

func TestAMintFailureIsReturnedNotPapered(t *testing.T) {
	minter := &recordingMinter{err: errors.New("signer broken")}
	if _, err := newTestIssuer(t, minter, newFakeObjectDeleter()).ForNewProject(context.Background(), platformStore(), "proj1"); err == nil {
		t.Fatal("a failed mint must fail the caller")
	}
}

func TestTheIssuerRefusesAConfigurationThatCannotIssue(t *testing.T) {
	open := func(context.Context, *domain.S3Credentials) (ObjectDeleter, error) {
		return newFakeObjectDeleter(), nil
	}
	cases := map[string]BackupCredentialIssuerConfig{
		"no minter":           {OpenStore: open, TTL: time.Hour, SourceTTL: time.Hour},
		"no store opener":     {Minter: &recordingMinter{}, TTL: time.Hour, SourceTTL: time.Hour},
		"no ttl":              {Minter: &recordingMinter{}, OpenStore: open, SourceTTL: time.Hour},
		"ttl beyond 7 days":   {Minter: &recordingMinter{}, OpenStore: open, TTL: 8 * 24 * time.Hour, SourceTTL: time.Hour},
		"no source ttl":       {Minter: &recordingMinter{}, OpenStore: open, TTL: time.Hour},
		"source beyond 7days": {Minter: &recordingMinter{}, OpenStore: open, TTL: time.Hour, SourceTTL: 8 * 24 * time.Hour},
	}
	for name, cfg := range cases {
		if _, err := NewBackupCredentialIssuer(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRenewalIsDueAtHalfLife(t *testing.T) {
	issuer := newTestIssuer(t, &recordingMinter{}, newFakeObjectDeleter())
	if got := issuer.RenewWithin(); got != 6*time.Hour {
		t.Fatalf("RenewWithin = %s, want half the 12h lifetime", got)
	}
}
