package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/objectcreds"
)

const mysqlTestPrefix = "moco/org1-proj1/proj1-mysql/"

// A MySQL project's backups live under MOCO's prefix, so its credentials are
// scoped to that prefix and handed out only while it is empty.
func TestIssuerScopesCredentialsToANamedPrefix(t *testing.T) {
	minter := &recordingMinter{}
	issuer := newTestIssuer(t, minter, newFakeObjectDeleter("proj1/cloud/base/1"))
	ctx := context.Background()
	if _, err := issuer.ForNewPrefix(ctx, platformStore(), "proj1", mysqlTestPrefix); err != nil {
		t.Fatalf("ForNewPrefix: %v", err)
	}
	if _, err := issuer.RenewPrefix(ctx, platformStore(), "proj1", mysqlTestPrefix); err != nil {
		t.Fatalf("RenewPrefix: %v", err)
	}
	if _, err := issuer.ForRestoreSourcePrefix(ctx, platformStore(), "proj1", mysqlTestPrefix); err != nil {
		t.Fatalf("ForRestoreSourcePrefix: %v", err)
	}
	want := []objectcreds.Scope{
		{Prefix: mysqlTestPrefix, Access: objectcreds.ReadWrite, TTL: 12 * time.Hour},
		{Prefix: mysqlTestPrefix, Access: objectcreds.ReadWrite, TTL: 12 * time.Hour},
		{Prefix: mysqlTestPrefix, Access: objectcreds.ReadOnly, TTL: 2 * time.Hour},
	}
	got := minter.minted()
	if len(got) != len(want) {
		t.Fatalf("minted %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("mint %d: %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestIssuerRefusesANamedPrefixThatHoldsObjects(t *testing.T) {
	issuer := newTestIssuer(t, &recordingMinter{}, newFakeObjectDeleter(mysqlTestPrefix+"20261011-000000/dump.tar"))
	_, err := issuer.ForNewPrefix(context.Background(), platformStore(), "proj1", mysqlTestPrefix)
	if !errors.Is(err, ErrBackupPrefixInUse) {
		t.Fatalf("err %v, want ErrBackupPrefixInUse", err)
	}
}
