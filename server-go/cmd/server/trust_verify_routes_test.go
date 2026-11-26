package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/studiooauth"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

type verifyFlowStub struct{}

func (verifyFlowStub) Configured() []string { return []string{"github"} }

func (verifyFlowStub) StartVerify(context.Context, string) (string, string, error) {
	return "https://github.example/authorize", "v.s", nil
}

func (verifyFlowStub) FinishVerify(context.Context, string, string, string) (studiooauth.Verification, error) {
	return studiooauth.Verification{}, studiooauth.ErrStateInvalid
}

type verifierStub struct{}

func (verifierStub) Begin(context.Context, string, bool) error { return nil }

func (verifierStub) Complete(context.Context, string, string) (trust.Attempt, error) {
	return trust.Attempt{}, nil
}

type readerStub struct{}

func (readerStub) Current(context.Context, string) (*trust.Record, error) { return nil, nil }

func (readerStub) Latest(context.Context, string) (*trust.Attempt, error) { return nil, nil }

func trustVerifyRouter(t *testing.T, flags features.Flags) (http.Handler, string) {
	t.Helper()
	cfg := config.AppConfig{DeploymentMode: "selfhosted"}
	router, who, _, _ := buildMatrixPlatform(t, cfg, func(deps *handlerDeps, _ *fakePlatform) {
		deps.features = flags
		deps.rlTrustVerify = func(next http.Handler) http.Handler { return next }
		deps.trustVerifyHandler = handler.NewTrustVerifyHandler(verifierStub{}, verifyFlowStub{}, readerStub{}, trust.Policy{MinAgeDays: 90}, time.Minute, "https://studio.example")
	})
	return router, who[callerDeveloper]
}

func TestTrustRoutesAreMountedBehindTheFlagAndAuth(t *testing.T) {
	on, token := trustVerifyRouter(t, features.NewStatic(features.LimitedTrial))
	if code, body := suspendedRequest(on, http.MethodGet, "/api/trust/", token); code != http.StatusOK || !strings.Contains(body, `"state":"limited"`) {
		t.Fatalf("flag on: %d %s", code, body)
	}
	if code, body := suspendedRequest(on, http.MethodPost, "/api/trust/github/start", token); code != http.StatusOK || !strings.Contains(body, "github.example") {
		t.Fatalf("start: %d %s", code, body)
	}
	if code, _ := suspendedRequest(on, http.MethodGet, "/api/trust/", ""); code != http.StatusUnauthorized {
		t.Errorf("anonymous: %d, want 401", code)
	}
	off, token := trustVerifyRouter(t, features.NewStatic())
	for _, tc := range []struct{ method, path string }{{http.MethodGet, "/api/trust/"}, {http.MethodPost, "/api/trust/github/start"}} {
		if code, _ := suspendedRequest(off, tc.method, tc.path, token); code != http.StatusNotFound {
			t.Errorf("flag off %s %s: %d, want 404", tc.method, tc.path, code)
		}
	}
}

// The one registered GitHub callback must reach verification; production wiring sets it.
func TestProductionHandsGitHubVerificationCallbacksToTheVerifyHandler(t *testing.T) {
	raw, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"oauth.SetVerifyCallback(verify.Callback)", "mountTrustRoutes(r, d)", "trustVerifyHandler:    trustVerify,"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("main.go does not contain %q", want)
		}
	}
}
