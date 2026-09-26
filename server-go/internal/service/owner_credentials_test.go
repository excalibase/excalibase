package service

import (
	"context"
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

const ownerProject = "owner-db"

func seedOwnerProject(t *testing.T) *ProvisioningService {
	t.Helper()
	svc, store, _ := setupProvisioningTest(t)
	port := 5432
	if err := store.Create(&domain.DatabaseInstance{
		ProjectID: ownerProject, Host: "host.local", Port: &port, DatabaseName: "mydb",
		Username: "owner", Password: "row-copy", SSLMode: "require", Status: "ACTIVE",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return svc
}

func vaultWithOwner(password string) *fakeVault {
	vault := newFakeVault()
	vault.Put(vaultCredentialPath(ownerProject, roleAdmin), map[string]string{"username": "owner", "password": password})
	return vault
}

func TestGetCredentialsHandsOutTheVaultedOwnerPassword(t *testing.T) {
	svc := seedOwnerProject(t)
	svc.SetVault(vaultWithOwner("from-vault"))

	creds, err := svc.GetCredentials(ownerProject)
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if creds.Password != "from-vault" {
		t.Errorf("password = %q, want the vault's", creds.Password)
	}
	if creds.ConnectionURL != "postgresql://owner:from-vault@host.local:5432/mydb?sslmode=require" {
		t.Errorf("connectionUrl = %q", creds.ConnectionURL)
	}
}

func TestGetCredentialsRefusesWhenTheVaultCannotAnswer(t *testing.T) {
	for name, vault := range map[string]func(*ProvisioningService){
		"no vault":       func(*ProvisioningService) {},
		"sealed vault":   func(s *ProvisioningService) { s.SetVault(&sealedVault{}) },
		"no record":      func(s *ProvisioningService) { s.SetVault(newFakeVault()) },
		"empty password": func(s *ProvisioningService) { s.SetVault(vaultWithOwner("")) },
	} {
		t.Run(name, func(t *testing.T) {
			svc := seedOwnerProject(t)
			vault(svc)
			if _, err := svc.GetCredentials(ownerProject); !errors.Is(err, ErrOwnerCredentialUnavailable) {
				t.Fatalf("got %v, want ErrOwnerCredentialUnavailable", err)
			}
		})
	}
}

func TestRegistrationRefusesWhenTheOwnerCredentialHasNowhereToBeKept(t *testing.T) {
	for name, vault := range map[string]func(*registrationHarness){
		"no vault":     func(h *registrationHarness) { h.svc.SetVault(nil) },
		"sealed vault": func(h *registrationHarness) { h.svc.SetVault(&sealedVault{}) },
	} {
		t.Run(name, func(t *testing.T) {
			h := newRegistrationHarness(t)
			vault(h)
			err := h.svc.RegisterProject(context.Background(), restoredInstance(), RegistrationOptions{})
			if !errors.Is(err, ErrOwnerCredentialUnavailable) {
				t.Fatalf("got %v, want ErrOwnerCredentialUnavailable", err)
			}
			if saved, _ := h.store.FindByProjectID(testRegProject); saved != nil {
				t.Error("a project whose owner credential was kept nowhere was registered")
			}
		})
	}
}

func TestARestoreWithoutAClusterSecretUsesTheSourcesVaultedOwnerPassword(t *testing.T) {
	mock := k8s.NewMockClient()
	reg := &fakeRegistrar{}
	adapter := newRestoreReadyAdapter(t, mock, reg)
	vault := newFakeVault()
	vault.Put(vaultCredentialPath("src", roleAdmin), map[string]string{"username": "app", "password": "source-owner"})
	svc := NewProvisioningService(nil, nil, nil)
	svc.SetVault(vault)
	adapter.SetOwnerCredentials(svc)

	src := sourceInstance()
	src.Username = "app"
	if _, err := adapter.Restore(context.Background(), src, domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"}); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(reg.calls) != 1 || reg.calls[0].Password != "source-owner" {
		t.Fatalf("restored owner credential: %+v", reg.calls)
	}
}

func TestARestoreWithoutAClusterSecretOrAVaultedOwnerIsRefused(t *testing.T) {
	mock := k8s.NewMockClient()
	reg := &fakeRegistrar{}
	adapter := newRestoreReadyAdapter(t, mock, reg)
	svc := NewProvisioningService(nil, nil, nil)
	svc.SetVault(newFakeVault())
	adapter.SetOwnerCredentials(svc)

	_, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if !errors.Is(err, ErrRestoreNotObserved) {
		t.Fatalf("got %v, want ErrRestoreNotObserved", err)
	}
	if len(reg.calls) != 0 {
		t.Error("a restore with no owner credential registered a project")
	}
}

func TestARestoreWithNoOwnerCredentialSourceIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	mock := k8s.NewMockClient()
	adapter := newRestoreReadyAdapter(t, mock, &fakeRegistrar{})
	adapter.SetOwnerCredentials(nil)

	_, err := adapter.Restore(context.Background(), sourceInstance(), domain.RestoreRequest{NewProjectName: "dst", TargetProjectID: "dst"})
	if !errors.Is(err, ErrOwnerCredentialUnavailable) {
		t.Fatalf("got %v, want ErrOwnerCredentialUnavailable", err)
	}
	if len(mock.Namespaces) != 0 || len(mock.CRDs) != 0 {
		t.Errorf("nothing may be created: ns=%v crds=%v", mock.Namespaces, mock.CRDs)
	}
}

func TestBackupServiceHandsTheOwnerCredentialsToItsAdapters(t *testing.T) {
	adapter := newRestoreReadyAdapter(t, k8s.NewMockClient(), &fakeRegistrar{})
	backups := NewBackupServiceWithAdapters(emptyInstanceStore(t), map[domain.DeploymentMode]BackupAdapter{domain.ModeK8s: adapter}, t.TempDir())
	svc := NewProvisioningService(nil, nil, nil)
	backups.SetOwnerCredentials(svc)
	if adapter.owners != svc {
		t.Error("the k8s adapter was not handed the owner credentials")
	}
}
