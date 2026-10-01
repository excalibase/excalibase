package service

import (
	"context"
	"errors"
	"testing"
)

// EXC-394: on an installation where the operator turned DocumentDB off, the
// gateway plugin is absent and a DocumentDB cluster never starts. The request
// is refused before anything is created, not failed five minutes later.
func TestADocumentDBProjectIsRefusedWhereDocumentDBIsNotInstalled(t *testing.T) {
	svc, store := documentDBProvisionService(t)
	svc.SetDocumentDBEnabled(false)

	_, err := svc.Provision(context.Background(), documentDBRequest("no-plugin", true))
	if !errors.Is(err, ErrDocumentDBNotInstalled) {
		t.Fatalf("error = %v, want ErrDocumentDBNotInstalled", err)
	}
	count, err := store.CountOrgProjects("org1")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("a refused request left %d projects", count)
	}
}

func TestAnOrdinaryProjectIsUnaffectedWhereDocumentDBIsNotInstalled(t *testing.T) {
	svc, _ := documentDBProvisionService(t)
	svc.SetDocumentDBEnabled(false)
	if _, err := svc.Provision(context.Background(), documentDBRequest("plain", false)); err != nil {
		t.Fatalf("provision: %v", err)
	}
}

func TestDocumentDBIsOffUntilTheInstallationSaysItIsOn(t *testing.T) {
	svc, _ := documentDBProvisionService(t)
	fresh := NewProvisioningService(svc.store, svc.factory, svc.k8sClient)
	if fresh.DocumentDBEnabled() {
		t.Fatal("DocumentDB must be off unless the installation enables it")
	}
}
