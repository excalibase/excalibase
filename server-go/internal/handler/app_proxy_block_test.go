package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/abuse"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/go-chi/chi/v5"
)

const proxyAppsPath = "/api/projects/" + appTestProject + "/apps/"

type auditSpy struct{ entries []*domain.AuditEntry }

func (a *auditSpy) LogAudit(_ context.Context, entry *domain.AuditEntry) error {
	a.entries = append(a.entries, entry)
	return nil
}

func proxyGuardRouter(t *testing.T, on bool) (chi.Router, *fakeAppStore, *auditSpy) {
	t.Helper()
	flags := features.NewStatic()
	if on {
		flags = features.NewStatic(features.ProxyBlock)
	}
	audit := &auditSpy{}
	store := newFakeAppStore()
	h := NewAppHandler(store, newFakeSources("storefront_db"), testAppRoute)
	h.SetAppLimits(fakeAppLimits{limit: testAppLimit})
	h.SetProxyGuard(abuse.NewAppGuard(flags, audit))
	r := chi.NewRouter()
	r.Route("/api/projects/{projectId}/apps", func(r chi.Router) { h.Routes(r) })
	return r, store, audit
}

func envLiteral(name string) map[string]any {
	return map[string]any{"name": name, "kind": "literal", "value": "x"}
}

func TestAppCreate_RefusesProxyByImageNameAndEnv(t *testing.T) {
	cases := map[string]func(map[string]any){
		"image": func(b map[string]any) { b["image"] = "ghcr.io/xtls/xray-core:latest" },
		"name":  func(b map[string]any) { b["name"] = "vless-node" },
		"env": func(b map[string]any) {
			b["env"] = []map[string]any{envLiteral("UUID"), envLiteral("WS_PATH")}
		},
	}
	for label, mutate := range cases {
		t.Run(label, func(t *testing.T) {
			r, store, audit := proxyGuardRouter(t, true)
			body := validAppBody()
			mutate(body)
			w := doAppRequest(t, r, http.MethodPost, proxyAppsPath, body)
			if w.Code != http.StatusForbidden {
				t.Fatalf("status %d, want 403: %s", w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "proxy") {
				t.Errorf("message does not say why: %s", w.Body.String())
			}
			if apps, _ := store.List(appTestProject); len(apps) != 0 {
				t.Errorf("a refused app was stored")
			}
			if len(audit.entries) != 1 || audit.entries[0].Action != abuse.AuditActionProxyRefused {
				t.Errorf("audit entries = %+v", audit.entries)
			}
		})
	}
}

func TestAppCreate_AllowsANormalAppAndAnOffFlag(t *testing.T) {
	r, _, audit := proxyGuardRouter(t, true)
	body := validAppBody()
	body["image"] = "node:22"
	body["env"] = []map[string]any{envLiteral("DATABASE_URL"), envLiteral("PORT")}
	if w := doAppRequest(t, r, http.MethodPost, proxyAppsPath, body); w.Code != http.StatusCreated {
		t.Fatalf("normal app: %d %s", w.Code, w.Body.String())
	}
	if len(audit.entries) != 0 {
		t.Errorf("audit entries for a normal app: %+v", audit.entries)
	}

	off, _, _ := proxyGuardRouter(t, false)
	proxy := validAppBody()
	proxy["image"] = "xray:1"
	if w := doAppRequest(t, off, http.MethodPost, proxyAppsPath, proxy); w.Code != http.StatusCreated {
		t.Fatalf("flag off: %d %s", w.Code, w.Body.String())
	}
}

func TestAppUpdate_RefusesAnImageOrEnvChangeThatMakesAProxy(t *testing.T) {
	r, store, audit := proxyGuardRouter(t, true)
	created := doAppRequest(t, r, http.MethodPost, proxyAppsPath, validAppBody())
	if created.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", created.Code, created.Body.String())
	}
	id := decodeApp(t, created).ID

	for label, patch := range map[string]map[string]any{
		"image": {"image": "teddysun/v2ray:5"},
		"env":   {"env": []map[string]any{envLiteral("NODE_UUID"), envLiteral("STREAM_PATH")}},
	} {
		w := doAppRequestWithVersion(t, r, http.MethodPatch, proxyAppsPath+id, patch, 1)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: status %d, want 403: %s", label, w.Code, w.Body.String())
		}
	}
	stored, _ := store.Get(appTestProject, id)
	if stored.Image != "ghcr.io/acme/storefront:1.4.2" || stored.Version != 1 {
		t.Errorf("a refused update changed the app: %+v", stored)
	}
	if len(audit.entries) != 2 {
		t.Errorf("audit entries = %d, want 2", len(audit.entries))
	}
	ok := doAppRequestWithVersion(t, r, http.MethodPatch, proxyAppsPath+id, map[string]any{"image": "node:22"}, 1)
	if ok.Code != http.StatusOK {
		t.Fatalf("normal update: %d %s", ok.Code, ok.Body.String())
	}
}

func TestAppSecret_RefusesASecretThatCompletesAProxyEnvironment(t *testing.T) {
	store, vault := newFakeAppStore(), newFakeVault()
	seedSecretApp(t, store)
	value := "x"
	app, _ := store.Get(appTestProject, secretTestApp)
	app.Env = append(app.Env, apphost.EnvVar{Name: "UUID", Kind: apphost.KindLiteral, Value: &value})
	if err := store.Update(app, app.Version); err != nil {
		t.Fatal(err)
	}
	audit := &auditSpy{}
	secrets := NewAppSecretHandler(store, vault)
	secrets.SetProxyGuard(abuse.NewAppGuard(features.NewStatic(features.ProxyBlock), audit))
	r := appSurfaceRouter(store, secrets, newFakeAppDeployer())

	if w := putSecret(r, secretTestApp, "WS_PATH", valueBody("/ws")); w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", w.Code, w.Body.String())
	}
	if w := putSecret(r, secretTestApp, "API_KEY", valueBody("k")); w.Code != http.StatusOK {
		t.Fatalf("a normal secret: %d %s", w.Code, w.Body.String())
	}
	if len(audit.entries) != 1 {
		t.Errorf("audit entries = %d, want 1", len(audit.entries))
	}
}

func TestAppDeploy_AProxyRefusalFromTheServiceIsA403(t *testing.T) {
	deployer := newFakeAppDeployer()
	deployer.deployErr = &abuse.ProxyRefusal{Finding: abuse.Finding{Signature: "xray"}}
	r := setupAppDeployRouter(t, deployer)
	w := doDeployRequest(t, r, http.MethodPost, "/api/projects/"+deployHandlerProject+"/apps/app-1/deploy")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", w.Code, w.Body.String())
	}
}

func TestTemplateDeploy_AProxyRefusalIsA403(t *testing.T) {
	failed := &service.TemplateFailedError{Step: "could not deploy app", Cause: &abuse.ProxyRefusal{Finding: abuse.Finding{Signature: "xray"}}, ServerFault: true}
	w := httptest.NewRecorder()
	writeTemplateError(w, failed)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "proxy") {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
}

func TestAppCreate_RefusesTheDisguisedFieldNotesNodeAndKeepsTheMessageGeneric(t *testing.T) {
	r, store, _ := proxyGuardRouter(t, true)
	body := validAppBody()
	body["name"] = "field-notes"
	body["image"] = "tkexcali02/field-notes@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	env := []map[string]any{}
	for _, name := range []string{"STREAM_PATH", "NODE_HOST", "NODE_LABEL", "TRANSPORT", "PORT", "NODE_UUID", "SUB_TOKEN"} {
		env = append(env, envLiteral(name))
	}
	body["env"] = env

	w := doAppRequest(t, r, http.MethodPost, proxyAppsPath, body)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status %d, want 403: %s", w.Code, w.Body.String())
	}
	for _, leak := range []string{"NODE_UUID", "STREAM_PATH", "SUB_TOKEN"} {
		if strings.Contains(w.Body.String(), leak) {
			t.Errorf("the refusal names %s: %s", leak, w.Body.String())
		}
	}
	if apps, _ := store.List(appTestProject); len(apps) != 0 {
		t.Errorf("a refused app was stored")
	}
}
