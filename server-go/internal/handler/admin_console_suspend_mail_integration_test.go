//go:build integration

package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/service"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	pgtest "github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	"github.com/go-chi/chi/v5"
)

// EXC-841: suspending through the admin route, on a real database, writes the
// row with its rule, stops the app and mails the account holder once.

type mailBox struct {
	mu   sync.Mutex
	sent []email.Message
	fail error
}

func (m *mailBox) Send(_ context.Context, msg email.Message) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail != nil {
		return "", m.fail
	}
	m.sent = append(m.sent, msg)
	return "id", nil
}

type pausedApps struct{ store *pgstore.Store }

func (p pausedApps) PauseApp(_ context.Context, projectID, appID string) (*apphost.App, error) {
	_, err := p.store.DB().Exec(`UPDATE apps SET status = $3 WHERE project_id = $1 AND id = $2`, projectID, appID, apphost.StatusStopped)
	return nil, err
}
func (pausedApps) ResumeApp(context.Context, string, string, string) (*apphost.App, error) {
	return nil, nil
}

func suspendMailRig(t *testing.T, on bool) (*pgstore.Store, http.Handler, *mailBox) {
	t.Helper()
	store := pgtest.New(t)
	e2eSeed(t, store)
	admin := &domain.User{ID: platformAdminID, Username: testutil.FixtureToken("mail-padmin"), Email: "padmin@test.com", Role: "platform_admin", Active: true, PasswordHash: testutil.FixturePasswordHash()}
	if err := store.CreateUser(context.Background(), admin); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE database_instances SET project_name = 'Shop' WHERE project_id = $1`, e2eProjectA); err != nil {
		t.Fatal(err)
	}
	app := &apphost.App{ID: "mail-app", ProjectID: e2eProjectA, Name: "miner", Image: "ghcr.io/acme/web:v1",
		Env: []apphost.EnvVar{}, Port: 8080, Replicas: 1, Tier: domain.Free, Status: apphost.StatusRunning}
	if err := apphost.NewPostgresAppStore(store.DB()).Create(app, 2); err != nil {
		t.Fatalf("create app: %v", err)
	}
	box := &mailBox{}
	queries := pgstore.NewAdminConsole(store.DB())
	suspensions := adminconsole.NewSuspensionService(queries, pausedApps{store}, store)
	suspensions.SetNotifier(service.NewSuspensionMail(box, service.SuspensionMailConfig{
		From: "noreply@example.test", SupportEmail: "support@example.test", PolicyURL: "https://example.test/acceptable-use"}),
		func(ctx context.Context) bool {
			return features.Enabled(ctx, features.NewStatic(flagIf(on)...), features.SuspensionEmails)
		})
	h := NewAdminConsoleHandler(adminconsole.NewConsole(queries, apphost.NewPostgresAppStore(store.DB()), nil), suspensions)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			ctx := auth.SetUser(req.Context(), admin)
			next.ServeHTTP(w, req.WithContext(auth.SetToken(ctx, &domain.AccessToken{Scopes: auth.ScopeSession})))
		})
	})
	r.Route("/api/admin", func(r chi.Router) { h.Routes(r) })
	return store, r, box
}

func flagIf(on bool) []features.Feature {
	if on {
		return []features.Feature{features.SuspensionEmails}
	}
	return nil
}

func suspendAlice(r http.Handler, body string) *httptest.ResponseRecorder {
	return consoleCall(r, http.MethodPost, "/api/admin/console/users/"+testAliceID+"/suspend", body)
}

func TestAdminSuspendRoute_RecordsTheRuleStopsTheAppAndMailsOnce(t *testing.T) {
	store, r, box := suspendMailRig(t, true)

	w := suspendAlice(r, `{"reason":"xmrig in the image","reasonCode":"crypto_mining"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("suspend: %d %s", w.Code, w.Body.String())
	}
	var code, reason string
	var active bool
	if err := store.DB().QueryRow(`SELECT s.reason_code, s.reason, u.active FROM abuse_suspensions s JOIN users u ON u.id = s.user_id WHERE s.user_id = $1`, testAliceID).Scan(&code, &reason, &active); err != nil {
		t.Fatalf("read suspension: %v", err)
	}
	if code != "crypto_mining" || reason != "xmrig in the image" || active {
		t.Fatalf("row = %q %q active=%v", code, reason, active)
	}
	var status string
	if err := store.DB().QueryRow(`SELECT status FROM apps WHERE id = 'mail-app'`).Scan(&status); err != nil || status != apphost.StatusStopped {
		t.Fatalf("app status = %q, %v", status, err)
	}
	if len(box.sent) != 1 {
		t.Fatalf("mails = %d, want exactly one", len(box.sent))
	}
	msg := box.sent[0]
	if msg.To[0] != "e2e-alice@test.com" || !strings.Contains(msg.TextBody, `Project "Shop": "miner"`) || !strings.Contains(msg.TextBody, "acceptable-use#crypto_mining") {
		t.Fatalf("mail = %+v", msg)
	}
	if strings.Contains(msg.TextBody, e2eProjectA) || strings.Contains(msg.TextBody, "xmrig") {
		t.Fatalf("mail leaks an internal detail: %s", msg.TextBody)
	}
}

func TestAdminSuspendRoute_FlagOffSuspendsInSilence(t *testing.T) {
	store, r, box := suspendMailRig(t, false)

	if w := suspendAlice(r, `{"reason":"proxy abuse","reasonCode":"proxy_vpn"}`); w.Code != http.StatusOK {
		t.Fatalf("suspend: %d %s", w.Code, w.Body.String())
	}
	if len(box.sent) != 0 {
		t.Fatalf("mails = %d with the flag off", len(box.sent))
	}
	if !mailMarked(t, store) {
		t.Fatal("a suspension made with the flag off must be settled, so a later flag never mails it")
	}
	var n int
	if err := store.DB().QueryRow(`SELECT count(*) FROM abuse_suspensions WHERE user_id = $1 AND reason_code = 'proxy_vpn'`, testAliceID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("suspension rows = %d, %v", n, err)
	}
}

func TestAdminSuspendRoute_AnUnknownCodeChangesNothing(t *testing.T) {
	store, r, box := suspendMailRig(t, true)

	if w := suspendAlice(r, `{"reason":"proxy abuse","reasonCode":"because"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("suspend: %d %s", w.Code, w.Body.String())
	}
	var n int
	if err := store.DB().QueryRow(`SELECT count(*) FROM abuse_suspensions`).Scan(&n); err != nil || n != 0 || len(box.sent) != 0 {
		t.Fatalf("rows = %d mails = %d, %v", n, len(box.sent), err)
	}
}

func TestAdminSuspendRoute_ARetryMailsOnceAfterAFailedSend(t *testing.T) {
	store, r, box := suspendMailRig(t, true)
	box.fail = errors.New("smtp down")
	body := `{"reason":"xmrig in the image","reasonCode":"crypto_mining"}`

	if w := suspendAlice(r, body); w.Code != http.StatusOK {
		t.Fatalf("first suspend: %d %s", w.Code, w.Body.String())
	}
	if mailMarked(t, store) {
		t.Fatal("a failed send must leave the mail owed")
	}

	box.fail = nil
	for i := 0; i < 3; i++ {
		if w := suspendAlice(r, body); w.Code != http.StatusOK {
			t.Fatalf("retry %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	if len(box.sent) != 1 || !mailMarked(t, store) {
		t.Fatalf("mails = %d marked = %v, want one mail then nothing", len(box.sent), mailMarked(t, store))
	}
	if !strings.Contains(box.sent[0].TextBody, `Project "Shop": "miner"`) {
		t.Fatalf("the retry must still name the stopped app: %s", box.sent[0].TextBody)
	}
}

func mailMarked(t *testing.T, store *pgstore.Store) bool {
	t.Helper()
	var marked bool
	if err := store.DB().QueryRow(`SELECT mail_sent_at IS NOT NULL FROM abuse_suspensions WHERE user_id = $1`, testAliceID).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	return marked
}

func TestAdminSuspendRoute_ConcurrentSuspendsMailOnce(t *testing.T) {
	store, r, box := suspendMailRig(t, true)
	body := `{"reason":"xmrig in the image","reasonCode":"crypto_mining"}`

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			suspendAlice(r, body)
		}()
	}
	wg.Wait()
	box.mu.Lock()
	sent := len(box.sent)
	box.mu.Unlock()
	if sent != 1 || !mailMarked(t, store) {
		t.Fatalf("mails = %d marked = %v, want exactly one", sent, mailMarked(t, store))
	}
}

func closureRig(t *testing.T) (*adminconsole.SuspensionService, *pgstore.AdminConsole, *mailBox) {
	t.Helper()
	store, _, _ := suspendMailRig(t, true)
	box := &mailBox{}
	queries := pgstore.NewAdminConsole(store.DB())
	svc := adminconsole.NewSuspensionService(queries, pausedApps{store}, store)
	svc.SetNotifier(service.NewSuspensionMail(box, service.SuspensionMailConfig{From: "noreply@example.test"}),
		func(context.Context) bool { return true })
	return svc, queries, box
}

// An owner suspended earlier is still mailed when the org is revoked; a second
// org is a second mail; the same org twice, one.
func TestClosureMail_OnARealDatabaseIsClaimedPerOrg(t *testing.T) {
	svc, _, box := closureRig(t)
	ctx := context.Background()
	if _, err := svc.Suspend(ctx, adminconsole.Actor{ID: platformAdminID}, testAliceID, adminconsole.Reason{Code: adminconsole.ReasonCryptoMining, Text: "xmrig"}); err != nil {
		t.Fatal(err)
	}
	_ = svc.NotifyOrgClosed(ctx, testAliceID, "org-a")
	_ = svc.NotifyOrgClosed(ctx, testAliceID, "org-a")
	_ = svc.NotifyOrgClosed(ctx, testAliceID, "org-b")
	if len(box.sent) != 3 {
		t.Fatalf("mails = %d, want the suspension mail and one closure mail per org (3)", len(box.sent))
	}
	if box.sent[1].Tags["kind"] != "org-closed" || box.sent[2].Tags["kind"] != "org-closed" {
		t.Fatalf("kinds = %v %v", box.sent[1].Tags, box.sent[2].Tags)
	}
}

func TestClosureMail_ConcurrentRevokesOfOneOrgMailOnce(t *testing.T) {
	svc, _, box := closureRig(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = svc.NotifyOrgClosed(context.Background(), testAliceID, "org-a")
		}()
	}
	wg.Wait()
	if len(box.sent) != 1 {
		t.Fatalf("mails = %d, want exactly one", len(box.sent))
	}
}

func TestClosureMail_AFailedSendIsOwedAgain(t *testing.T) {
	svc, _, box := closureRig(t)
	box.fail = errors.New("smtp down")
	if err := svc.NotifyOrgClosed(context.Background(), testAliceID, "org-a"); err == nil {
		t.Fatal("a failed send must be returned")
	}
	box.fail = nil
	if err := svc.NotifyOrgClosed(context.Background(), testAliceID, "org-a"); err != nil {
		t.Fatal(err)
	}
	if len(box.sent) != 1 {
		t.Fatalf("mails = %d, want the retry to send it", len(box.sent))
	}
}
