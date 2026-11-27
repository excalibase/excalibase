package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/credits"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// stubLedger is the credit store the handler tests stand on; each field is the answer to one call.
type stubLedger struct {
	credits.Ledger
	account    credits.Account
	entries    []credits.Entry
	statement  credits.Statement
	trial      credits.TrialState
	termsSeen  string
	termsErr   error
	grant      credits.GrantResult
	orgs       map[string]bool
	adjustErr  error
	adjusted   []credits.Adjustment
	readErr    error
	entryBound struct {
		before int64
		limit  int
	}
}

func (l *stubLedger) CreditAccount(context.Context, string) (credits.Account, error) {
	return l.account, l.readErr
}

func (l *stubLedger) CreditEntries(_ context.Context, _ string, before int64, limit int) ([]credits.Entry, error) {
	l.entryBound.before, l.entryBound.limit = before, limit
	return l.entries, l.readErr
}

func (l *stubLedger) CreditStatement(context.Context, string, time.Time) (credits.Statement, error) {
	return l.statement, l.readErr
}

func (l *stubLedger) CreditTrialState(context.Context, string) (credits.TrialState, error) {
	return l.trial, nil
}

func (l *stubLedger) CreditTermsVersion(context.Context, string) (string, error) {
	return l.termsSeen, nil
}

func (l *stubLedger) AcceptCreditTerms(context.Context, string, string, time.Time) error {
	return l.termsErr
}

func (l *stubLedger) GrantCreditTrial(context.Context, string, credits.Rules, time.Time) (credits.GrantResult, error) {
	return l.grant, nil
}

func (l *stubLedger) CreditOrgExists(_ context.Context, orgID string) (bool, error) {
	return l.orgs[orgID], nil
}

func (l *stubLedger) AdjustCredit(_ context.Context, adjustment credits.Adjustment, _ time.Time) (credits.Entry, error) {
	l.adjusted = append(l.adjusted, adjustment)
	return credits.Entry{ID: 7, OrgID: adjustment.OrgID, Amount: adjustment.Amount}, l.adjustErr
}

var unitRules = credits.Rules{TrialAmount: credits.FromDollars(300), TrialDays: 90, TrialCap: 40, AdminCap: credits.FromDollars(1000), TermsVersion: "terms-1"}

// unitMembers lets one person into one org.
type unitMembers struct{ orgID, userID string }

func (m unitMembers) GetOrgMember(_ context.Context, orgID, userID string) (*domain.OrgMember, error) {
	if orgID == m.orgID && userID == m.userID {
		return &domain.OrgMember{OrgID: orgID, UserID: userID}, nil
	}
	return nil, errors.New("not a member")
}

func unitCredits(ledger *stubLedger) *CreditsHandler {
	return NewCreditsHandler(credits.NewService(ledger, unitRules, nil), unitMembers{"o1", "u1"}, "https://example.test/terms")
}

func passGate(next http.Handler) http.Handler { return next }

func unitRouter(h *CreditsHandler) http.Handler {
	r := chi.NewRouter()
	r.Route("/orgs/{orgId}", func(r chi.Router) { h.OrgRoutes(r, passGate) })
	r.Route("/admin", func(r chi.Router) { h.ConsoleRoutes(r, passGate) })
	r.Route("/credits", func(r chi.Router) { h.PersonRoutes(r, passGate) })
	return r
}

func session() *domain.AccessToken { return &domain.AccessToken{Scopes: auth.ScopeSession} }

func serve(h http.Handler, method, path, body string, user *domain.User, token *domain.AccessToken, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := req.Context()
	if user != nil {
		ctx = auth.SetUser(ctx, user)
	}
	if token != nil {
		ctx = auth.SetToken(ctx, token)
	}
	req = req.WithContext(ctx)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

var member = &domain.User{ID: "u1"}

func TestAMembersOverviewCarriesTheBalanceAndTheTermsLink(t *testing.T) {
	ledger := &stubLedger{account: credits.Account{OrgID: "o1", Balance: 5}, trial: credits.TrialState{State: "granted"}}
	w := serve(unitRouter(unitCredits(ledger)), http.MethodGet, "/orgs/o1/credits/", "", member, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var body struct {
		BalanceMicros int64  `json:"balanceMicros"`
		TermsURL      string `json:"termsUrl"`
		Trial         struct{ State string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.BalanceMicros != 5 || body.TermsURL != "https://example.test/terms" || body.Trial.State != "granted" {
		t.Fatalf("%v %+v", err, body)
	}
}

func TestStrangersAndAnonymousCallersReadNoCredit(t *testing.T) {
	h := unitRouter(unitCredits(&stubLedger{}))
	for _, path := range []string{"/orgs/o1/credits/", "/orgs/o1/credits/entries", "/orgs/o1/credits/statement"} {
		if w := serve(h, http.MethodGet, path, "", &domain.User{ID: "stranger"}, nil, nil); w.Code != http.StatusNotFound {
			t.Errorf("%s as a stranger: %d", path, w.Code)
		}
		if w := serve(h, http.MethodGet, path, "", nil, nil, nil); w.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous: %d", path, w.Code)
		}
	}
}

func TestAPlatformServicePrincipalReadsAnyOrgsCredit(t *testing.T) {
	admin := &domain.User{ID: "svc", Role: "platform_admin"}
	token := &domain.AccessToken{Scopes: "read", Permissions: []string{"credits:read"}}
	if !auth.IsPlatformServicePrincipal(admin, token) {
		t.Fatal("the fixture must be a platform service principal")
	}
	w := serve(unitRouter(unitCredits(&stubLedger{})), http.MethodGet, "/orgs/other/credits/", "", admin, token, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
}

func TestAReadFailureAnswersWithoutTheDatabaseDetail(t *testing.T) {
	h := unitRouter(unitCredits(&stubLedger{readErr: errors.New("pq: password authentication failed for user bob")}))
	for _, path := range []string{"/orgs/o1/credits/", "/orgs/o1/credits/entries", "/orgs/o1/credits/statement"} {
		w := serve(h, http.MethodGet, path, "", member, nil, nil)
		if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "bob") {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
}

func TestEntriesPageByBeforeAndLimitAndIgnoreNonsense(t *testing.T) {
	ledger := &stubLedger{entries: []credits.Entry{{ID: 3}}}
	h := unitRouter(unitCredits(ledger))
	if w := serve(h, http.MethodGet, "/orgs/o1/credits/entries?before=50&limit=10", "", member, nil, nil); w.Code != http.StatusOK ||
		ledger.entryBound.before != 50 || ledger.entryBound.limit != 10 {
		t.Fatalf("%d %+v", w.Code, ledger.entryBound)
	}
	serve(h, http.MethodGet, "/orgs/o1/credits/entries?before=-9&limit=abc", "", member, nil, nil)
	if ledger.entryBound.before != 0 {
		t.Fatalf("a negative cursor is read as the start: %d", ledger.entryBound.before)
	}
}

func TestTheStatementIsJSONOrACSVDownloadForTheMonthAsked(t *testing.T) {
	ledger := &stubLedger{statement: credits.Statement{Month: "2026-12", Entries: []credits.Entry{{ID: 1, Kind: credits.KindGrantTrial, Amount: 5, Actor: "system", Reason: "sign-up credit"}}}}
	h := unitRouter(unitCredits(ledger))
	if w := serve(h, http.MethodGet, "/orgs/o1/credits/statement?month=2026-12", "", member, nil, nil); w.Code != http.StatusOK ||
		!strings.Contains(w.Header().Get("Content-Type"), "json") {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	w := serve(h, http.MethodGet, "/orgs/o1/credits/statement?month=2026-12&format=csv", "", member, nil, nil)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "excalibase-credits-o1-2026-12.csv") || !strings.Contains(w.Body.String(), "sign-up credit") {
		t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
	}
	if w := serve(h, http.MethodGet, "/orgs/o1/credits/statement", "", member, nil, nil); w.Code != http.StatusOK {
		t.Fatalf("no month means this month: %d", w.Code)
	}
	if w := serve(h, http.MethodGet, "/orgs/o1/credits/statement?month=December", "", member, nil, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("a month that is not YYYY-MM: %d", w.Code)
	}
}

func TestAcceptingTheTermsNeedsASessionAndTheCurrentVersion(t *testing.T) {
	h := unitRouter(unitCredits(&stubLedger{grant: credits.GrantResult{Outcome: credits.OutcomeGranted}}))
	post := func(user *domain.User, token *domain.AccessToken, body string) *httptest.ResponseRecorder {
		return serve(h, http.MethodPost, "/credits/terms", body, user, token, nil)
	}
	if w := post(member, &domain.AccessToken{Scopes: "read"}, `{"version":"terms-1"}`); w.Code != http.StatusForbidden {
		t.Fatalf("an access token is refused: %d", w.Code)
	}
	if w := post(member, session(), `{"version":"terms-1"}`); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"accepted":true`) {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := post(member, session(), `{"version":"old"}`); w.Code != http.StatusConflict {
		t.Fatalf("stale terms: %d", w.Code)
	}
	if w := post(member, session(), `not json`); w.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", w.Code)
	}
}

func TestAcceptingTheTermsWithoutAUserIsRefused(t *testing.T) {
	if w := serve(http.HandlerFunc(unitCredits(&stubLedger{}).AcceptTerms), http.MethodPost, "/", `{"version":"terms-1"}`, nil, session(), nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("%d", w.Code)
	}
}

func TestAStoreFailureWhileAcceptingTermsIsHiddenFromTheCaller(t *testing.T) {
	h := unitRouter(unitCredits(&stubLedger{termsErr: errors.New("pq: deadlock")}))
	w := serve(h, http.MethodPost, "/credits/terms", `{"version":"terms-1"}`, member, session(), nil)
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "deadlock") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

const adminBody = `{"amountMicros":50000000,"reason":"goodwill after the outage"}`

func adminPost(h http.Handler, body, key string, token *domain.AccessToken) *httptest.ResponseRecorder {
	headers := map[string]string{}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	return serve(h, http.MethodPost, "/admin/console/orgs/o1/credits", body, &domain.User{ID: "admin-1", Role: "platform_admin"}, token, headers)
}

func TestAnAdminGrantNeedsASessionAnIdempotencyKeyAndAValidBody(t *testing.T) {
	ledger := &stubLedger{orgs: map[string]bool{"o1": true}}
	h := unitRouter(unitCredits(ledger))
	if w := adminPost(h, adminBody, "key-12345678", &domain.AccessToken{Scopes: "read"}); w.Code != http.StatusForbidden {
		t.Fatalf("an access token: %d", w.Code)
	}
	if w := adminPost(h, adminBody, "", session()); w.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", w.Code)
	}
	if w := adminPost(h, adminBody, "bad key!", session()); w.Code != http.StatusBadRequest {
		t.Fatalf("a malformed key: %d", w.Code)
	}
	if w := adminPost(h, `nope`, "key-12345678", session()); w.Code != http.StatusBadRequest {
		t.Fatalf("a bad body: %d", w.Code)
	}
	if w := adminPost(h, adminBody, "key-12345678", session()); w.Code != http.StatusOK || len(ledger.adjusted) != 1 || ledger.adjusted[0].Actor != "admin-1" {
		t.Fatalf("%d %s %+v", w.Code, "", ledger.adjusted)
	}
}

func TestEachAdminRefusalHasItsOwnStatus(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		ledger *stubLedger
		want   int
	}{
		{"a short reason", `{"amountMicros":5,"reason":"x"}`, &stubLedger{orgs: map[string]bool{"o1": true}}, http.StatusBadRequest},
		{"a zero amount", `{"amountMicros":0,"reason":"goodwill after the outage"}`, &stubLedger{orgs: map[string]bool{"o1": true}}, http.StatusBadRequest},
		{"above the cap", `{"amountMicros":9999999999999,"reason":"goodwill after the outage"}`, &stubLedger{orgs: map[string]bool{"o1": true}}, http.StatusBadRequest},
		{"an unknown org", adminBody, &stubLedger{orgs: map[string]bool{}}, http.StatusNotFound},
		{"more than the balance", adminBody, &stubLedger{orgs: map[string]bool{"o1": true}, adjustErr: credits.ErrInsufficientBalance}, http.StatusConflict},
		{"a replay with another request", adminBody, &stubLedger{orgs: map[string]bool{"o1": true}, adjustErr: credits.ErrIdempotencyConflict}, http.StatusConflict},
		{"a store failure", adminBody, &stubLedger{orgs: map[string]bool{"o1": true}, adjustErr: errors.New("pq: boom")}, http.StatusInternalServerError},
	}
	for _, c := range cases {
		if w := adminPost(unitRouter(unitCredits(c.ledger)), c.body, "key-12345678", session()); w.Code != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, w.Code, w.Body, c.want)
		}
	}
}
