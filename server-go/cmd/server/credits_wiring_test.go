package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/handler"
)

// matrixLedger answers every credit read from nothing, so a request that gets
// past the gates reaches a handler that answers instead of failing on a store.
type matrixLedger struct{ credits.Ledger }

func (matrixLedger) CreditAccount(_ context.Context, orgID string) (credits.Account, error) {
	return credits.Account{OrgID: orgID, Lots: []credits.Lot{}}, nil
}
func (matrixLedger) CreditEntries(context.Context, string, int64, int) ([]credits.Entry, error) {
	return []credits.Entry{}, nil
}
func (matrixLedger) CreditStatement(_ context.Context, _ string, month time.Time) (credits.Statement, error) {
	return credits.Statement{Month: month.Format("2006-01"), Entries: []credits.Entry{}}, nil
}
func (matrixLedger) CreditTrialState(context.Context, string) (credits.TrialState, error) {
	return credits.TrialState{State: "none"}, nil
}
func (matrixLedger) CreditTermsVersion(context.Context, string) (string, error) { return "", nil }
func (matrixLedger) AcceptCreditTerms(context.Context, string, string, time.Time) error {
	return nil
}
func (matrixLedger) GrantCreditTrial(context.Context, string, credits.Rules, time.Time) (credits.GrantResult, error) {
	return credits.GrantResult{Outcome: credits.OutcomeNeedsTerms}, nil
}
func (matrixLedger) CreditOrgExists(_ context.Context, orgID string) (bool, error) {
	return orgID == matrixOrgA || orgID == matrixOrgB, nil
}
func (matrixLedger) AdjustCredit(_ context.Context, adjustment credits.Adjustment, _ time.Time) (credits.Entry, error) {
	return credits.Entry{ID: 1, OrgID: adjustment.OrgID, Amount: adjustment.Amount}, nil
}

func matrixCreditsHandler(members *fakePlatform) *handler.CreditsHandler {
	rules := credits.Rules{TrialAmount: credits.FromDollars(300), TrialDays: 90, TrialCap: 40, AdminCap: credits.FromDollars(1000), TermsVersion: "v1"}
	return handler.NewCreditsHandler(credits.NewService(matrixLedger{}, rules, nil), members, "https://example.test/terms")
}

func TestCreditsRulesComeFromConfig(t *testing.T) {
	cfg := config.AppConfig{Credits: config.CreditsConfig{TrialDollars: 123, TrialDays: 7, TrialCap: 3, AdminGrantCapDollars: 9, TermsVersion: "v9"}}
	rules := creditRules(cfg)
	if rules.TrialAmount != credits.FromDollars(123) || rules.TrialDays != 7 || rules.TrialCap != 3 || rules.AdminCap != credits.FromDollars(9) || rules.TermsVersion != "v9" {
		t.Fatalf("%+v", rules)
	}
}

func TestCreditsSweeperRunsOnlyWithItsFlag(t *testing.T) {
	svc := credits.NewService(matrixLedger{}, credits.Rules{}, nil)
	stop := startCreditsSweeper(config.AppConfig{}, nil, svc)
	stop()
	if creditsOn(config.AppConfig{}) {
		t.Fatal("credits are off unless the flag is set")
	}
	t.Setenv("FEATURE_CREDITS", "true")
	if !creditsOn(config.AppConfig{DarkFeatures: config.Load().DarkFeatures}) {
		t.Fatal("FEATURE_CREDITS=true turns credits on")
	}
}

func TestFlagOffEveryCreditRouteAnswersAsUnmounted(t *testing.T) {
	router, token := darkFeatureRouter(t)
	for _, probe := range creditProbes() {
		w := probeDark(router, probe, token)
		if w.Code != http.StatusNotFound || w.Body.String() != unmountedBody {
			t.Errorf("credits off: %s %s answered %d %q", probe.method, probe.path, w.Code, w.Body.String())
		}
	}
}

func TestFlagOnCreditRoutesAreServed(t *testing.T) {
	router, token := darkFeatureRouter(t, features.Credits)
	for _, probe := range creditProbes() {
		if w := probeDark(router, probe, token); w.Body.String() == unmountedBody {
			t.Errorf("credits on: %s %s is still unmounted", probe.method, probe.path)
		}
	}
}

func creditProbes() []darkProbe {
	return darkRoutes[features.Credits]
}

func TestCreditsAdminRouteNeedsAnIdempotencyKeyAndASession(t *testing.T) {
	router, _, admins, _ := buildMatrixPlatform(t, config.AppConfig{DeploymentMode: "selfhosted"}, func(deps *handlerDeps, platform *fakePlatform) {})
	path := "/api/admin/console/orgs/" + matrixOrgA + "/credits"
	post := func(token, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	const body = `{"amountMicros": 5000000, "reason": "goodwill after the outage"}`
	if w := post(admins[callerAdminSession], "", body); w.Code != http.StatusBadRequest {
		t.Errorf("no key: %d %s", w.Code, w.Body.String())
	}
	if w := post(admins[callerAdminSession], "key-12345678", body); w.Code != http.StatusOK {
		t.Errorf("a good grant: %d %s", w.Code, w.Body.String())
	}
	if w := post(admins[callerAdminSession], "key-12345678", `{"amountMicros": 5000000, "reason": "short"}`); w.Code != http.StatusBadRequest {
		t.Errorf("a short reason: %d %s", w.Code, w.Body.String())
	}
	if w := post(admins[callerAdminSession], "key-12345678", `{"amountMicros": 1000000001, "reason": "goodwill after the outage"}`); w.Code != http.StatusBadRequest {
		t.Errorf("above the cap: %d %s", w.Code, w.Body.String())
	}
	if w := post(admins[callerAdminWrite], "key-12345678", body); w.Code != http.StatusForbidden {
		t.Errorf("an access token must not grant: %d %s", w.Code, w.Body.String())
	}
}

func TestBootRefusesCreditsWithoutTheContainerGate(t *testing.T) {
	on := func(flags ...features.Feature) config.AppConfig { return config.AppConfig{DarkFeatures: flags} }
	if err := creditsBootCheck(on(features.Credits)); err == nil || !strings.Contains(err.Error(), "FEATURE_LIMITEDTRIAL") {
		t.Fatalf("credits on and the gate off must refuse to boot: %v", err)
	}
	if err := creditsBootCheck(on(features.Credits, features.LimitedTrial)); err != nil {
		t.Fatalf("both on: %v", err)
	}
	if err := creditsBootCheck(on(features.LimitedTrial)); err != nil {
		t.Fatalf("the gate alone: %v", err)
	}
	if err := creditsBootCheck(on()); err != nil {
		t.Fatalf("both off: %v", err)
	}
}
