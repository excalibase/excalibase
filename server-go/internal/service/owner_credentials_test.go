package service

import (
	"context"
	"errors"
	"net/url"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
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

	creds, err := svc.GetCredentials(context.Background(), ownerProject)
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
			if _, err := svc.GetCredentials(context.Background(), ownerProject); !errors.Is(err, ErrOwnerCredentialUnavailable) {
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

// EXC-636: a reserved character in the password does not break the URL.
func TestGetCredentialsConnectionURLEscapesThePassword(t *testing.T) {
	const password = "p@ss:w/rd?#%x"
	svc := seedOwnerProject(t)
	svc.SetVault(vaultWithOwner(password))

	creds, err := svc.GetCredentials(context.Background(), ownerProject)
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	parsed, err := url.Parse(creds.ConnectionURL)
	if err != nil {
		t.Fatalf("connectionUrl does not parse: %v", err)
	}
	if got, _ := parsed.User.Password(); got != password || parsed.User.Username() != "owner" {
		t.Errorf("login = %q / %q", parsed.User.Username(), got)
	}
	if parsed.Host != "host.local:5432" || parsed.Path != "/mydb" || parsed.Query().Get("sslmode") != "require" {
		t.Errorf("host %q path %q query %q", parsed.Host, parsed.Path, parsed.RawQuery)
	}
}

// EXC-675: an Admin is handed the SELECT-only login beside the owner's, only
// while its feature is on.
func TestGetCredentialsHandsOutTheReaderWhileItsFeatureIsOn(t *testing.T) {
	for _, on := range []bool{false, true} {
		svc := seedOwnerProject(t)
		vault := vaultWithOwner("from-vault")
		vault.Put(vaultCredentialPath(ownerProject, roleReader), map[string]string{"username": roleReader, "password": "reader-pass"})
		svc.SetVault(vault)
		if on {
			svc.SetFeatures(features.NewStatic(features.DBReadRole))
		}
		creds, err := svc.GetCredentials(context.Background(), ownerProject)
		if err != nil {
			t.Fatalf("GetCredentials: %v", err)
		}
		gotReader := creds.ReaderUsername == roleReader && creds.ReaderPassword == "reader-pass"
		if gotReader != on || (!on && (creds.ReaderUsername != "" || creds.ReaderPassword != "")) {
			t.Errorf("feature %v: reader = %q/%q", on, creds.ReaderUsername, creds.ReaderPassword)
		}
	}
}

func TestGetCredentialsWithoutAFiledReaderNamesNone(t *testing.T) {
	svc := seedOwnerProject(t)
	svc.SetVault(vaultWithOwner("from-vault"))
	svc.SetFeatures(features.NewStatic(features.DBReadRole))
	creds, err := svc.GetCredentials(context.Background(), ownerProject)
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if creds.ReaderUsername != "" || creds.ReaderPassword != "" {
		t.Errorf("a project with no reader was handed one: %q", creds.ReaderUsername)
	}
}
