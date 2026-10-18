package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// ctx.db runs in the runtime's main thread, which opens its pool from its own
// environment: the project's runtime is given the owner login's URL (EXC-625).
func TestRuntimeSpec_CarriesTheProjectDatabaseURL(t *testing.T) {
	f := setupEgressHandler(t)
	vault := newFakeVault()
	vault.data["projects/"+testEgressProject+"/credentials/admin"] = map[string]string{
		"host": testEgressDBHost, "port": "5432", "database": "app", "username": "owner", "password": "p4ss",
	}
	f.handler.SetVault(vault)
	deployEgressFn(t, f, "hello")

	want := "postgres://owner:p4ss@" + testEgressDBHost + ":5432/app?sslmode=require"
	if got := f.k8s.DenoSpecs[testEgressNS].DatabaseURL; got != want {
		t.Fatalf("runtime DatabaseURL = %q want %q", got, want)
	}
}

// The runtime's pool speaks Postgres only; a MySQL login is not handed to it.
func TestRuntimeSpec_MySQLProjectGetsNoPoolURL(t *testing.T) {
	f := setupEgressHandler(t)
	vault := newFakeVault()
	vault.data["projects/"+testEgressProject+"/credentials/admin"] = map[string]string{
		"host": "mysql-primary", "port": "3306", "database": "app", "username": "owner", "password": "p4ss",
		domain.CredentialEngineKey: domain.CredentialEngineMySQL,
	}
	f.handler.SetVault(vault)
	deployEgressFn(t, f, "hello")

	if got := f.k8s.DenoSpecs[testEgressNS].DatabaseURL; got != "" {
		t.Fatalf("a MySQL project's runtime must not get a pool URL, got %q", got)
	}
}

func TestRuntimeSpec_NoDatabaseLoginNoURL(t *testing.T) {
	f := setupEgressHandler(t)
	f.handler.SetVault(newFakeVault())
	deployEgressFn(t, f, "hello")
	if got := f.k8s.DenoSpecs[testEgressNS].DatabaseURL; got != "" {
		t.Fatalf("no login, no URL; got %q", got)
	}
}

// After the owner's password is rotated the runtime is given the new URL and
// every function is redeployed with it (EXC-625).
func TestCredentialsRotated_RendersTheNewURLAndRedeploys(t *testing.T) {
	f := setupEgressHandler(t)
	vault := newFakeVault()
	path := "projects/" + testEgressProject + "/credentials/admin"
	vault.data[path] = map[string]string{
		"host": testEgressDBHost, "port": "5432", "database": "app", "username": "owner", "password": "old",
	}
	f.handler.SetVault(vault)
	deployEgressFn(t, f, "hello")

	vault.data[path]["password"] = "new"
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	f.handler.CredentialsRotated(req, testEgressProject)

	want := "postgres://owner:new@" + testEgressDBHost + ":5432/app?sslmode=require"
	if got := f.k8s.DenoSpecs[testEgressNS].DatabaseURL; got != want {
		t.Fatalf("runtime DatabaseURL = %q want %q", got, want)
	}
	if got := (*f.scripts)[testEgressProject+"__hello"].Secrets["EXCALIBASE_DB_URL"]; got != want {
		t.Fatalf("redeployed function EXCALIBASE_DB_URL = %q want %q", got, want)
	}
}

// A project that never deployed a function has no runtime to tell.
func TestCredentialsRotated_NoRuntimeIsLeftAlone(t *testing.T) {
	f := setupEgressHandler(t)
	f.handler.SetVault(newFakeVault())
	f.handler.CredentialsRotated(httptest.NewRequest(http.MethodPost, "/", nil), testEgressProject)
	if _, ok := f.k8s.DenoSpecs[testEgressNS]; ok {
		t.Fatal("no function deployed, no runtime created")
	}
}
