package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

type bootSetupTokenStore struct {
	hasAdmin  bool
	tokenHash string
}

func (s *bootSetupTokenStore) HasPlatformAdmin(context.Context) (bool, error) {
	return s.hasAdmin, nil
}

func (s *bootSetupTokenStore) StoreSetupTokenHash(_ context.Context, tokenHash string) error {
	s.tokenHash = tokenHash
	return nil
}

func (s *bootSetupTokenStore) CreateFirstAdmin(context.Context, string, *domain.User) error {
	return nil
}

// Any value of at least auth.MinSetupTokenLength characters.
var operatorSetupToken = strings.Repeat("t", 40)

func bootSetupToken(t *testing.T, mode string, store *bootSetupTokenStore, preset string) (string, error) {
	t.Helper()
	var logged strings.Builder
	err := establishFirstAdminSetupToken(t.Context(), config.AppConfig{ProvisionerMode: mode}, store, preset,
		func(format string, args ...any) { logged.WriteString(format) })
	return logged.String(), err
}

// EXC-451 / GA risk 44: on Kubernetes the chart always supplies SETUP_TOKEN, so
// a missing one is a broken install; provisioning must not mint a token and
// print it to a log anyone with log access can read.
func TestSetupTokenBoot_Kubernetes_NoAdminNoToken_Refuses(t *testing.T) {
	store := &bootSetupTokenStore{}
	logged, err := bootSetupToken(t, "k8s", store, "")
	if !errors.Is(err, errSetupTokenRequired) {
		t.Fatalf("want errSetupTokenRequired, got %v", err)
	}
	if !strings.Contains(err.Error(), "SETUP_TOKEN") {
		t.Errorf("error should name SETUP_TOKEN: %v", err)
	}
	if store.tokenHash != "" {
		t.Error("no token hash may be stored when the start is refused")
	}
	if logged != "" {
		t.Errorf("nothing may be logged, got %q", logged)
	}
}

func TestSetupTokenBoot_Kubernetes_OperatorToken_AdoptedNotLogged(t *testing.T) {
	store := &bootSetupTokenStore{}
	logged, err := bootSetupToken(t, "k8s", store, operatorSetupToken)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if store.tokenHash == "" {
		t.Error("the operator's token hash must be stored")
	}
	if logged != "" {
		t.Errorf("the operator's token must not be logged, got %q", logged)
	}
}

// A restart after the first admin exists needs no token at all.
func TestSetupTokenBoot_Kubernetes_AdminExists_NoTokenNeeded(t *testing.T) {
	store := &bootSetupTokenStore{hasAdmin: true}
	logged, err := bootSetupToken(t, "k8s", store, "")
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if store.tokenHash != "" || logged != "" {
		t.Errorf("nothing to do once an admin exists: hash=%q logged=%q", store.tokenHash, logged)
	}
}

// Any mode but docker is treated as Kubernetes (PROVISIONER_MODE defaults to k8s).
func TestSetupTokenBoot_UnsetMode_TreatedAsKubernetes(t *testing.T) {
	if _, err := bootSetupToken(t, "", &bootSetupTokenStore{}, ""); !errors.Is(err, errSetupTokenRequired) {
		t.Fatalf("want errSetupTokenRequired, got %v", err)
	}
}

// The docker provisioner keeps its behaviour: docker-compose.aio.yml already
// requires the operator's SETUP_TOKEN; a bare dev run still gets a generated one.
func TestSetupTokenBoot_Docker_NoToken_GeneratesAndLogsOnce(t *testing.T) {
	store := &bootSetupTokenStore{}
	logged, err := bootSetupToken(t, "docker", store, "")
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if store.tokenHash == "" || logged == "" {
		t.Errorf("docker without SETUP_TOKEN generates and logs a token: hash=%q logged=%q", store.tokenHash, logged)
	}
}

func TestSetupTokenBoot_Docker_OperatorToken_NotLogged(t *testing.T) {
	store := &bootSetupTokenStore{}
	logged, err := bootSetupToken(t, "docker", store, operatorSetupToken)
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if logged != "" {
		t.Errorf("the operator's token must not be logged, got %q", logged)
	}
}
