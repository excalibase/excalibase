package mcpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func deployWith(t *testing.T, routes *fakeRoutes, args map[string]any) (string, map[string]any) {
	t.Helper()
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	args["project_id"], args["app_id"] = testProjectA, "web"
	res := callTool(t, cs, "deploy_app", args)
	if res.IsError {
		return resultText(res), nil
	}
	return "", structured(t, res)
}

func TestDeployFromAPrivateRegistryWithACredentialRunsTheImage(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/apps/web/deploy", 202, `{"id":"d1","status":"pending","digest":"sha256:aa"}`)
	failure, out := deployWith(t, routes, map[string]any{"image": "registry.acme.test/team/web:3"})
	if failure != "" || out["deploy"] == nil {
		t.Fatalf("failure %q out %v", failure, out)
	}
	if routes.calls[0].Body != `{"commitSha":"","image":"registry.acme.test/team/web:3"}` {
		t.Fatalf("body = %s", routes.calls[0].Body)
	}
}

func TestDeployRefusedForWantOfARegistryCredentialSaysWhereToAddOne(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/apps/web/deploy", 422, `{"error":"the registry refused access to the image; save a credential for its registry"}`)
	routes.on(http.MethodGet, projectsA+"/registry-credentials/", 200, `[{"registry":"ghcr.io"}]`)
	failure, _ := deployWith(t, routes, map[string]any{"image": "registry.acme.test/team/web:3"})
	for _, want := range []string{"registry.acme.test", "no credential", "Registry credentials", "https://app.example.test/project/proj-a/containers#registry-credentials"} {
		if !strings.Contains(failure, want) {
			t.Errorf("error lacks %q: %s", want, failure)
		}
	}
}

func TestDeployRefusedDespiteACredentialSaysItWasRejected(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodPost, projectsA+"/apps/web/deploy", 422, `{"error":"the registry refused access to the image; save a credential for its registry"}`)
	routes.on(http.MethodGet, projectsA+"/registry-credentials/", 200, `[{"registry":"registry.acme.test"}]`)
	failure, _ := deployWith(t, routes, map[string]any{"image": "registry.acme.test/team/web:3"})
	if !strings.Contains(failure, "saved for registry.acme.test") || !strings.Contains(failure, "refused") {
		t.Fatalf("error = %s", failure)
	}
}

func TestDeployWithAnImageOnAServerWithoutThePipelineFallsBackToUpdateThenDeploy(t *testing.T) {
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/apps/web/", 200, storedWeb)
	routes.on(http.MethodPatch, projectsA+"/apps/web/", 200, `{"id":"web","version":8}`)
	// A deploy with a body answers 404 where the pipeline is off; Studio's own
	// bodyless deploy still works, so the image goes through the app first.
	deployCalls := 0
	cs := session(t, deployFlagOff(routes, &deployCalls), &recordingAudit{}, writeCaller())
	res := callTool(t, cs, "deploy_app", map[string]any{"project_id": testProjectA, "app_id": "web", "image": "ghcr.io/a/web:2"})
	out := structured(t, res)
	if deployCalls != 2 || out["deploy"] == nil || out["unpinned"] == nil {
		t.Fatalf("deploy calls %d out %v", deployCalls, out)
	}
	if routes.calls[len(routes.calls)-1].Body != "" && routes.calls[len(routes.calls)-1].Body != "null" {
		t.Fatalf("the second deploy must carry no body: %q", routes.calls[len(routes.calls)-1].Body)
	}
}

// deployFlagOff answers a deploy that has a body with 404, and one without with a deploy.
func deployFlagOff(routes *fakeRoutes, deployCalls *int) http.Handler {
	routes.on(http.MethodPost, projectsA+"/apps/web/deploy", 202, `{"id":"d1","status":"pending","image":"ghcr.io/a/web:2"}`)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == projectsA+"/apps/web/deploy" {
			*deployCalls++
			if r.ContentLength > 0 {
				routes.mu.Lock()
				routes.calls = append(routes.calls, recordedCall{Method: r.Method, Path: r.URL.Path, Body: "with-body"})
				routes.mu.Unlock()
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":"not found"}`))
				return
			}
		}
		routes.ServeHTTP(w, r)
	})
}

func TestDeployOnAClusterWithoutCiliumSaysItIsTheOperatorsProblem(t *testing.T) {
	for _, message := range []string{
		"create app ingress policy: the server could not find the requested resource",
		`create app egress policy: no matches for kind "CiliumNetworkPolicy" in version "cilium.io/v2"`,
	} {
		routes := newFakeRoutes()
		routes.on(http.MethodPost, projectsA+"/apps/web/deploy", 500, `{"error":"`+strings.ReplaceAll(message, `"`, `\"`)+`"}`)
		failure, _ := deployWith(t, routes, map[string]any{})
		if !strings.Contains(failure, "Cilium") || !strings.Contains(failure, "platform operator") || strings.Contains(failure, "could not find the requested resource") {
			t.Errorf("%s: error = %s", message, failure)
		}
	}
}

func statusSession(t *testing.T, appJSON, deploysJSON string, extra func(*fakeRoutes)) map[string]any {
	t.Helper()
	routes := newFakeRoutes()
	routes.on(http.MethodGet, projectsA+"/apps/web/", 200, appJSON)
	routes.on(http.MethodGet, projectsA+"/apps/web/deploys", 200, deploysJSON)
	if extra != nil {
		extra(routes)
	}
	cs := session(t, routes, &recordingAudit{}, writeCaller())
	return structured(t, callTool(t, cs, "get_deploy_status", map[string]any{"project_id": testProjectA, "app_id": "web"}))
}

func TestDeployStatusDuringARolloutIsNotThePreviousFailure(t *testing.T) {
	out := statusSession(t, `{"id":"web","status":"FAILED","url":"https://x.test"}`,
		`[{"id":"d2","status":"rolling"},{"id":"d1","status":"failed","failureReason":"boom"}]`, nil)
	app, _ := out["app"].(map[string]any)
	if out["state"] != "rolling out" || app["status"] != "ROLLING_OUT" {
		t.Fatalf("out = %v", out)
	}
}

func TestDeployStatusOnAFailedDeployKeepsItsReason(t *testing.T) {
	out := statusSession(t, `{"id":"web","status":"FAILED"}`, `[{"id":"d1","status":"failed","failureReason":"boom"}]`, nil)
	if out["state"] != "failed" {
		t.Fatalf("out = %v", out)
	}
}

func TestDeployStatusExplainsAnOperatorSideCiliumFailure(t *testing.T) {
	out := statusSession(t, `{"id":"web","status":"FAILED"}`,
		`[{"id":"d1","status":"failed","failureReason":"create app ingress policy: the server could not find the requested resource"}]`, nil)
	if hint, _ := out["failureHint"].(string); !strings.Contains(hint, "Cilium") {
		t.Fatalf("out = %v", out)
	}
}

func TestDeployStatusIsSucceededOnlyWhenTheURLAnswers(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer site.Close()
	out := statusSession(t, `{"id":"web","status":"ACTIVE","url":"`+site.URL+`"}`, `[{"id":"d1","status":"succeeded"}]`, nil)
	probe, _ := out["urlProbe"].(map[string]any)
	if out["state"] != "succeeded" || probe["answered"] != true || probe["status"] != float64(200) {
		t.Fatalf("out = %v", out)
	}
}

func TestDeployStatusSaysRunningWhenTheEdgeAnswersNotReady(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer site.Close()
	out := statusSession(t, `{"id":"web","status":"ACTIVE","url":"`+site.URL+`"}`, `[{"id":"d1","status":"succeeded"}]`, nil)
	probe, _ := out["urlProbe"].(map[string]any)
	if out["state"] != "running, URL not answering yet" || !strings.Contains(probe["reason"].(string), "503") {
		t.Fatalf("out = %v", out)
	}
}

func TestDeployStatusNamesAPendingCertificateAsTheReason(t *testing.T) {
	site := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer site.Close()
	out := statusSession(t, `{"id":"web","status":"ACTIVE","url":"`+site.URL+`"}`, `[{"id":"d1","status":"succeeded"}]`, func(r *fakeRoutes) {
		r.on(http.MethodGet, projectsA+"/apps/web/certificate", 200, `{"hostname":"web.example.test","status":"issuing"}`)
	})
	probe, _ := out["urlProbe"].(map[string]any)
	if out["state"] != "running, URL not answering yet" || !strings.Contains(probe["reason"].(string), "certificate") {
		t.Fatalf("out = %v", out)
	}
}

func TestDeployStatusNamesAnUnreachableAddress(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	address := site.URL
	site.Close()
	out := statusSession(t, `{"id":"web","status":"ACTIVE","url":"`+address+`"}`, `[{"id":"d1","status":"succeeded"}]`, nil)
	probe, _ := out["urlProbe"].(map[string]any)
	if out["state"] != "running, URL not answering yet" || probe["reason"] == "" {
		t.Fatalf("out = %v", out)
	}
}

func TestDeployStatusWithoutAnAddressStaysSucceeded(t *testing.T) {
	out := statusSession(t, `{"id":"web","status":"ACTIVE"}`, `[{"id":"d1","status":"succeeded"}]`, nil)
	if out["state"] != "succeeded" || out["urlProbe"] != nil {
		t.Fatalf("out = %v", out)
	}
}
