package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
)

// EXC-662 step 7: the copied tenant database holds factors sealed with the
// source's MFA key, so the copy gets that key at its own path. A source that
// never had a key gives a copy with MFA off; any other failure fails the
// restore, so its compensations run.

type getFailingVault struct{ *fakeVault }

func (getFailingVault) Get(string) (map[string]string, error) { return nil, errors.New("vault sealed") }

func mfaCarrier(t *testing.T, vault vaultclient.VaultClient) (*ProvisioningService, *recordingSettings) {
	t.Helper()
	svc := NewProvisioningService(emptyInstanceStore(t), provisioner.NewFactory(), nil)
	settings := &recordingSettings{}
	svc.SetRestoredSettings(settings)
	svc.SetMigrationHistory(&recordingHistory{})
	if vault != nil {
		svc.SetVault(vault)
	}
	return svc, settings
}

func sourceKey(t *testing.T, vault *fakeVault) string {
	t.Helper()
	key, err := domain.NewMFAKey()
	if err != nil {
		t.Fatal(err)
	}
	vault.data[domain.MFAKeyPath("src")] = map[string]string{domain.MFAKeyField: key}
	return key
}

func TestRestoredCopyGetsTheSourcesMFAKeyAtItsOwnPath(t *testing.T) {
	vault := newFakeVault()
	key := sourceKey(t, vault)
	svc, settings := mfaCarrier(t, vault)
	if err := svc.CarryRestoredSettings(context.Background(), nil, "src", "dst"); err != nil {
		t.Fatal(err)
	}
	if got := vault.data[domain.MFAKeyPath("dst")]; len(got) != 1 || got[domain.MFAKeyField] != key {
		t.Fatal("the copy does not hold the source's key")
	}
	if vault.data[domain.MFAKeyPath("src")][domain.MFAKeyField] != key || len(settings.mfaOff) != 0 {
		t.Fatalf("source changed or MFA turned off: %v", settings.mfaOff)
	}
}

// A restore that fails later removes the key it gave the copy.
func TestAFailedRestoreRemovesTheCopysMFAKey(t *testing.T) {
	vault := newFakeVault()
	sourceKey(t, vault)
	svc, _ := mfaCarrier(t, vault)
	pc := provisioner.NewProvisionContext(nil, nil)
	if err := svc.CarryRestoredSettings(context.Background(), pc, "src", "dst"); err != nil {
		t.Fatal(err)
	}
	pc.Rollback(context.Background())
	if _, kept := vault.data[domain.MFAKeyPath("dst")]; kept {
		t.Fatal("the copy's key outlived the failed restore")
	}
	if _, kept := vault.data[domain.MFAKeyPath("src")]; !kept {
		t.Fatal("the rollback removed the source's key")
	}
}

func TestRestoredCopyKeepsAnIdenticalKeyItAlreadyHolds(t *testing.T) {
	vault := newFakeVault()
	key := sourceKey(t, vault)
	vault.data[domain.MFAKeyPath("dst")] = map[string]string{domain.MFAKeyField: key}
	svc, settings := mfaCarrier(t, vault)
	pc := provisioner.NewProvisionContext(nil, nil)
	if err := svc.CarryRestoredSettings(context.Background(), pc, "src", "dst"); err != nil || len(settings.mfaOff) != 0 {
		t.Fatalf("err=%v off=%v", err, settings.mfaOff)
	}
	pc.Rollback(context.Background())
	if _, kept := vault.data[domain.MFAKeyPath("dst")]; !kept {
		t.Fatal("a rollback removed a key this restore did not write")
	}
}

func TestASourceThatNeverHadMFAGivesACopyWithMFAOff(t *testing.T) {
	for name, vault := range map[string]vaultclient.VaultClient{"no key": newFakeVault(), "no vault": nil} {
		svc, settings := mfaCarrier(t, vault)
		if err := svc.CarryRestoredSettings(context.Background(), nil, "src", "dst"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(settings.mfaOff) != 1 || settings.mfaOff[0] != "dst" {
			t.Errorf("%s: MFA off for %v, want the copy", name, settings.mfaOff)
		}
	}
}

func TestRestoreFailsWhenTheMFAKeyCannotBeCarried(t *testing.T) {
	other, _ := domain.NewMFAKey()
	cases := map[string]func() (vaultclient.VaultClient, *fakeVault){
		"an invalid source key": func() (vaultclient.VaultClient, *fakeVault) {
			v := newFakeVault()
			v.data[domain.MFAKeyPath("src")] = map[string]string{domain.MFAKeyField: "short"}
			return v, v
		},
		"an unreadable vault": func() (vaultclient.VaultClient, *fakeVault) { v := newFakeVault(); return getFailingVault{v}, v },
		"a refused write": func() (vaultclient.VaultClient, *fakeVault) {
			v := newFakeVault()
			sourceKey(t, v)
			return failingPutVault{v}, v
		},
		"a different key already at the copy": func() (vaultclient.VaultClient, *fakeVault) {
			v := newFakeVault()
			sourceKey(t, v)
			v.data[domain.MFAKeyPath("dst")] = map[string]string{domain.MFAKeyField: other}
			return v, v
		},
	}
	for name, build := range cases {
		wired, backing := build()
		svc, settings := mfaCarrier(t, wired)
		if err := svc.CarryRestoredSettings(context.Background(), nil, "src", "dst"); err == nil {
			t.Errorf("%s: the restore went on", name)
		}
		if len(settings.mfaOff) != 0 {
			t.Errorf("%s: fell back to MFA off: %v", name, settings.mfaOff)
		}
		if name == "a different key already at the copy" && backing.data[domain.MFAKeyPath("dst")][domain.MFAKeyField] != other {
			t.Errorf("%s: the copy's key was replaced", name)
		}
	}
}

// If the copy's MFA cannot be switched off, the restore fails rather than
// activate a copy that claims a key it does not have.
func TestRestoreFailsWhenTheCopysMFACannotBeSwitchedOff(t *testing.T) {
	svc, settings := mfaCarrier(t, newFakeVault())
	settings.disableErr = errors.New("platform database down")
	if err := svc.CarryRestoredSettings(context.Background(), nil, "src", "dst"); err == nil {
		t.Fatal("CarryRestoredSettings = nil")
	}
}

func (failingPutVault) PutIfAbsent(string, map[string]string) (bool, error) {
	return false, errors.New("vault down")
}
