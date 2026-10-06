package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/testutil/fakestore"
)

func domainsConfig(issuer string) config.AppConfig {
	return config.AppConfig{AppHostingEnabled: true, AppDomain: "apps.example.com", AppIngressClass: "haproxy",
		AppIngressFromNamespace: "haproxy-controller", AppDomainIssuer: issuer, AppDomainResolver: "127.0.0.1:53"}
}

func TestVerifyAppDomainIssuer_RefusesAnIssuerThatCannotIssue(t *testing.T) {
	kube := k8s.NewMockClient()
	if err := verifyAppDomainIssuer(context.Background(), domainsConfig(""), nil); err != nil {
		t.Fatalf("custom domains off need nothing: %v", err)
	}
	if err := verifyAppDomainIssuer(context.Background(), domainsConfig("le"), nil); err == nil {
		t.Fatal("an issuer with no cluster to check it must refuse")
	}
	kube.IssuerReadyErr = k8s.ErrClusterIssuerNotReady
	if err := verifyAppDomainIssuer(context.Background(), domainsConfig("le"), kube); !errors.Is(err, k8s.ErrClusterIssuerNotReady) {
		t.Fatalf("err = %v", err)
	}
	kube.IssuerReadyErr = nil
	if err := verifyAppDomainIssuer(context.Background(), domainsConfig("le"), kube); err != nil {
		t.Fatalf("ready issuer: %v", err)
	}
}

func TestCustomDomainsStayOffWithoutAnIssuer(t *testing.T) {
	deploys := service.NewAppDeployService(nil, nil, k8s.NewMockClient(), fakestore.NewInstances(), nil, k8s.AppRenderOptions{})
	if svc := buildAppDomainService(domainsConfig(""), nil, k8s.NewMockClient(), fakestore.NewInstances(), deploys); svc != nil {
		t.Fatal("no issuer, no custom domains")
	}
	if newAppDomainHandler(nil) != nil {
		t.Fatal("no service, no routes")
	}
	startAppDomainSweeper(domainsConfig(""), nil, nil)()
}

func TestCustomDomainsOnWithAnIssuer(t *testing.T) {
	deploys := service.NewAppDeployService(nil, nil, k8s.NewMockClient(), fakestore.NewInstances(), nil, k8s.AppRenderOptions{})
	db, err := sql.Open("postgres", "postgres://x:x@127.0.0.1:1/x?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	svc := buildAppDomainService(domainsConfig("le"), db, k8s.NewMockClient(), fakestore.NewInstances(), deploys)
	if svc == nil || newAppDomainHandler(svc) == nil {
		t.Fatal("an issuer turns custom domains on")
	}
	startAppDomainSweeper(domainsConfig("le"), nil, svc)()
}

func TestAppDomainResolver(t *testing.T) {
	if server, err := appDomainResolver(domainsConfig("le")); err != nil || server != "127.0.0.1:53" {
		t.Fatalf("configured = %q %v", server, err)
	}
	cfg := domainsConfig("le")
	cfg.AppDomainResolver = ""
	if _, err := appDomainResolver(cfg); err != nil {
		t.Logf("host has no resolv.conf: %v", err)
	}
}

func TestConfigReportsCustomDomains(t *testing.T) {
	for issuer, want := range map[string]bool{"": false, "le": true} {
		w := httptest.NewRecorder()
		serveConfig(domainsConfig(issuer), nil)(w, httptest.NewRequest(http.MethodGet, "/api/config", nil))
		if got := strings.Contains(w.Body.String(), `"customDomains":true`); got != want {
			t.Errorf("issuer %q: body %s", issuer, w.Body.String())
		}
	}
}
