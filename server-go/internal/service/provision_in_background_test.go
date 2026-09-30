package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A project build outlasts what a browser waits for one request. Accepting it
// answers once the project row exists, with the project still PROVISIONING;
// the build runs on and the caller follows it through the project's status.
func TestProvisionInBackground_AnswersWithTheProvisioningProjectThenBuildsIt(t *testing.T) {
	svc, store := documentDBProvisionService(t)
	svc.SetVault(newFakeVault())
	var build func()
	svc.runInBackground = func(f func()) { build = f }

	resp, err := svc.ProvisionInBackground(context.Background(), documentDBRequest("built-later", false))
	if err != nil {
		t.Fatalf("ProvisionInBackground: %v", err)
	}
	if resp.ProjectID == "" || resp.Status != string(domain.StatusProvisioning) || resp.ProjectName != "built-later" {
		t.Fatalf("answer = %+v, want the new project, PROVISIONING", resp)
	}
	if inst, _ := store.FindByProjectID(resp.ProjectID); inst == nil || inst.Status != string(domain.StatusProvisioning) {
		t.Fatalf("stored project = %+v, want PROVISIONING before the build runs", inst)
	}
	if build == nil {
		t.Fatal("no build was started")
	}

	build()
	inst, _ := store.FindByProjectID(resp.ProjectID)
	if inst == nil || inst.Status != string(domain.StatusActive) {
		t.Fatalf("after the build: %+v, want ACTIVE", inst)
	}
}

// The build is not tied to the request: the caller going away does not stop it.
func TestProvisionInBackground_TheBuildOutlivesTheRequest(t *testing.T) {
	svc, store := documentDBProvisionService(t)
	svc.SetVault(newFakeVault())
	var build func()
	svc.runInBackground = func(f func()) { build = f }
	ctx, cancel := context.WithCancel(context.Background())

	resp, err := svc.ProvisionInBackground(ctx, documentDBRequest("caller-left", false))
	if err != nil {
		t.Fatalf("ProvisionInBackground: %v", err)
	}
	cancel()
	build()
	if inst, _ := store.FindByProjectID(resp.ProjectID); inst == nil || inst.Status != string(domain.StatusActive) {
		t.Fatalf("after the caller left: %+v, want ACTIVE", inst)
	}
}

// What can be refused before anything is built is still refused in the answer.
func TestProvisionInBackground_RefusesAnInvalidRequestWithoutStartingABuild(t *testing.T) {
	svc, _ := documentDBProvisionService(t)
	started := false
	svc.runInBackground = func(func()) { started = true }
	req := documentDBRequest("no-major", false)
	req.PostgresVersion = ""

	if _, err := svc.ProvisionInBackground(context.Background(), req); err == nil {
		t.Fatal("want the refusal")
	}
	if started {
		t.Error("a refused request started a build")
	}
}
