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
// source's MFA key, so the copy gets that key at its own path. When the key
// cannot be carried the copy keeps MFA off rather than claim a key auth
// cannot read.

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

func TestRestoredCopyGetsTheSourcesMFAKeyAtItsOwnPath(t *testing.T) {
	vault := newFakeVault()
	key, _ := domain.NewMFAKey()
	vault.data[domain.MFAKeyPath("src")] = map[string]string{domain.MFAKeyField: key}
	svc, settings := mfaCarrier(t, vault)
	if err := svc.CarryRestoredSettings(context.Background(), "src", "dst"); err != nil {
		t.Fatal(err)
	}
	if got := vault.data[domain.MFAKeyPath("dst")]; len(got) != 1 || got[domain.MFAKeyField] != key {
		t.Fatal("the copy does not hold the source's key")
	}
	if vault.data[domain.MFAKeyPath("src")][domain.MFAKeyField] != key || len(settings.mfaOff) != 0 {
		t.Fatalf("source changed or MFA turned off: %v", settings.mfaOff)
	}
}

func TestRestoredCopyKeepsAnIdenticalKeyItAlreadyHolds(t *testing.T) {
	vault := newFakeVault()
	key, _ := domain.NewMFAKey()
	vault.data[domain.MFAKeyPath("src")] = map[string]string{domain.MFAKeyField: key}
	vault.data[domain.MFAKeyPath("dst")] = map[string]string{domain.MFAKeyField: key}
	svc, settings := mfaCarrier(t, vault)
	if err := svc.CarryRestoredSettings(context.Background(), "src", "dst"); err != nil || len(settings.mfaOff) != 0 {
		t.Fatalf("err=%v off=%v", err, settings.mfaOff)
	}
}

func TestRestoredCopyKeepsMFAOffWhenTheKeyCannotBeCarried(t *testing.T) {
	valid, _ := domain.NewMFAKey()
	other, _ := domain.NewMFAKey()
	cases := map[string]func() (vaultclient.VaultClient, *fakeVault){
		"no key at the source": func() (vaultclient.VaultClient, *fakeVault) { v := newFakeVault(); return v, v },
		"an invalid source key": func() (vaultclient.VaultClient, *fakeVault) {
			v := newFakeVault()
			v.data[domain.MFAKeyPath("src")] = map[string]string{domain.MFAKeyField: "short"}
			return v, v
		},
		"an unreadable vault": func() (vaultclient.VaultClient, *fakeVault) { v := newFakeVault(); return getFailingVault{v}, v },
		"a refused write": func() (vaultclient.VaultClient, *fakeVault) {
			v := newFakeVault()
			v.data[domain.MFAKeyPath("src")] = map[string]string{domain.MFAKeyField: valid}
			return failingPutVault{v}, v
		},
		"a different key already at the copy": func() (vaultclient.VaultClient, *fakeVault) {
			v := newFakeVault()
			v.data[domain.MFAKeyPath("src")] = map[string]string{domain.MFAKeyField: valid}
			v.data[domain.MFAKeyPath("dst")] = map[string]string{domain.MFAKeyField: other}
			return v, v
		},
	}
	for name, build := range cases {
		wired, backing := build()
		svc, settings := mfaCarrier(t, wired)
		if err := svc.CarryRestoredSettings(context.Background(), "src", "dst"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(settings.mfaOff) != 1 || settings.mfaOff[0] != "dst" {
			t.Errorf("%s: MFA off for %v, want the copy", name, settings.mfaOff)
		}
		if name == "a different key already at the copy" && backing.data[domain.MFAKeyPath("dst")][domain.MFAKeyField] != other {
			t.Errorf("%s: the copy's key was replaced", name)
		}
	}
}

func TestRestoredCopyWithoutAVaultKeepsMFAOff(t *testing.T) {
	svc, settings := mfaCarrier(t, nil)
	if err := svc.CarryRestoredSettings(context.Background(), "src", "dst"); err != nil || len(settings.mfaOff) != 1 {
		t.Fatalf("err=%v off=%v", err, settings.mfaOff)
	}
}

// If MFA can be neither carried nor switched off, the restore fails rather
// than activate a copy that claims a key it does not have.
func TestRestoreFailsWhenTheCopysMFACannotBeSwitchedOff(t *testing.T) {
	svc, settings := mfaCarrier(t, newFakeVault())
	settings.disableErr = errors.New("platform database down")
	if err := svc.CarryRestoredSettings(context.Background(), "src", "dst"); err == nil {
		t.Fatal("CarryRestoredSettings = nil")
	}
}

// A copy path the vault cannot answer for is never written blind.
type targetUnreadableVault struct{ *fakeVault }

func (v targetUnreadableVault) Get(path string) (map[string]string, error) {
	if path == domain.MFAKeyPath("dst") {
		return nil, errors.New("vault sealed")
	}
	return v.fakeVault.Get(path)
}

func TestRestoredCopyNeverWritesAKeyWhereItCannotRead(t *testing.T) {
	backing := newFakeVault()
	key, _ := domain.NewMFAKey()
	backing.data[domain.MFAKeyPath("src")] = map[string]string{domain.MFAKeyField: key}
	svc, settings := mfaCarrier(t, targetUnreadableVault{backing})
	if err := svc.CarryRestoredSettings(context.Background(), "src", "dst"); err != nil {
		t.Fatal(err)
	}
	if _, written := backing.data[domain.MFAKeyPath("dst")]; written || len(settings.mfaOff) != 1 {
		t.Fatalf("written=%v off=%v", written, settings.mfaOff)
	}
}
