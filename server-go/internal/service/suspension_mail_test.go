package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/adminconsole"
)

func suspensionMailFixture(code adminconsole.ReasonCode) (*SuspensionMail, *sentMail, adminconsole.SuspensionNotice) {
	sender := &sentMail{}
	mail := NewSuspensionMail(sender, SuspensionMailConfig{
		From: "noreply@example.test", FromName: "Excalibase", SupportEmail: "support@example.test",
		PolicyURL: "https://example.test/acceptable-use",
	})
	return mail, sender, adminconsole.SuspensionNotice{
		Email: "user@example.test", Code: code,
		Projects: []adminconsole.NoticeProject{{Name: "Shop", Apps: []string{"web", "worker"}}, {Name: "Lab", Apps: []string{"miner"}}},
	}
}

var suspensionCodes = []adminconsole.ReasonCode{
	adminconsole.ReasonProxyVPN, adminconsole.ReasonCryptoMining, adminconsole.ReasonSpamOrPhishing,
	adminconsole.ReasonDisposableEmail, adminconsole.ReasonOther,
}

func TestSuspensionMail_OneMailPerReasonWithItsOwnRule(t *testing.T) {
	bodies := map[string]adminconsole.ReasonCode{}
	for _, code := range suspensionCodes {
		mail, sender, notice := suspensionMailFixture(code)
		if err := mail.Suspended(context.Background(), notice); err != nil {
			t.Fatalf("%s: %v", code, err)
		}
		if len(sender.messages) != 1 || len(sender.messages[0].To) != 1 || sender.messages[0].To[0] != "user@example.test" {
			t.Fatalf("%s: messages = %+v, want exactly one to the account holder", code, sender.messages)
		}
		msg := sender.messages[0]
		if prior, dup := bodies[msg.TextBody]; dup {
			t.Errorf("%s and %s share one body", code, prior)
		}
		bodies[msg.TextBody] = code
		if msg.Tags["kind"] != "account-suspended" || msg.Tags["reason"] != string(code) {
			t.Errorf("%s: tags = %v", code, msg.Tags)
		}
		if !strings.Contains(msg.TextBody, "https://example.test/acceptable-use#"+string(code)) {
			t.Errorf("%s: body lacks the policy section anchor: %s", code, msg.TextBody)
		}
	}
}

func TestSuspensionMail_SaysWhatWasStoppedHowToAppealAndWhatHappensToData(t *testing.T) {
	mail, sender, notice := suspensionMailFixture(adminconsole.ReasonCryptoMining)
	if err := mail.Suspended(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	body := sender.messages[0].TextBody
	for _, want := range []string{"Shop", "web", "worker", "Lab", "miner", "support@example.test", "not deleted", "kept as they are"} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q:\n%s", want, body)
		}
	}
	if sender.messages[0].ReplyTo != "support@example.test" {
		t.Errorf("reply-to = %q, an appeal is a reply", sender.messages[0].ReplyTo)
	}
}

func TestSuspensionMail_NamesNothingInternal(t *testing.T) {
	for _, code := range suspensionCodes {
		mail, sender, notice := suspensionMailFixture(code)
		_ = mail.Suspended(context.Background(), notice)
		msg := sender.messages[0]
		for _, banned := range []string{"namespace", "kubectl", "abuse_suspensions", "user_id", "proj-", "admin", "IP ", "digest", "sha256", "pod", "cluster"} {
			if strings.Contains(strings.ToLower(msg.TextBody+msg.Subject), strings.ToLower(banned)) {
				t.Errorf("%s: mail mentions %q:\n%s", code, banned, msg.TextBody)
			}
		}
	}
}

func TestSuspensionMail_WithNoAppsStillTellsTheAccountIsStopped(t *testing.T) {
	mail, sender, notice := suspensionMailFixture(adminconsole.ReasonDisposableEmail)
	notice.Projects = nil
	if err := mail.Suspended(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sender.messages[0].TextBody, "No apps were running") {
		t.Errorf("body = %s", sender.messages[0].TextBody)
	}
}

func TestSuspensionMail_ReturnsTheSendFailure(t *testing.T) {
	mail, sender, notice := suspensionMailFixture(adminconsole.ReasonOther)
	sender.err = errors.New("smtp down")
	if err := mail.Suspended(context.Background(), notice); err == nil {
		t.Fatal("a failed send must be returned so it is counted")
	}
}

func TestSuspensionMail_NeverGoesToBlank(t *testing.T) {
	mail, sender, notice := suspensionMailFixture(adminconsole.ReasonOther)
	notice.Email = ""
	if err := mail.Suspended(context.Background(), notice); err == nil || len(sender.messages) != 0 {
		t.Fatalf("an account without an address is an error, nothing is sent: %v %d", err, len(sender.messages))
	}
}

// A revoke deletes the organisation, so its mail must not say data is kept.
func TestSuspensionMail_AnOrgClosureSaysTheDataIsBeingDeleted(t *testing.T) {
	mail, sender, notice := suspensionMailFixture(adminconsole.ReasonOther)
	mail.cfg.BackupsKept = true
	notice.OrgClosed = true
	if err := mail.Suspended(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	msg := sender.messages[0]
	for _, want := range []string{"closed", "being deleted", "14 days", "support@example.test", "https://example.test/acceptable-use"} {
		if !strings.Contains(msg.TextBody, want) {
			t.Errorf("closure mail lacks %q:\n%s", want, msg.TextBody)
		}
	}
	for _, banned := range []string{"not deleted", "kept as they are", "suspended"} {
		if strings.Contains(msg.TextBody+msg.Subject, banned) {
			t.Errorf("closure mail says %q, which is false for a closure:\n%s", banned, msg.TextBody)
		}
	}
	if msg.Tags["kind"] != "org-closed" || msg.Subject != "Your Excalibase organisation was closed" {
		t.Errorf("subject %q tags %v", msg.Subject, msg.Tags)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("messages = %d", len(sender.messages))
	}
}

// Names are typed by the user. A line break in one must never start a line of its own.
func TestSuspensionMail_ANameCannotInjectALine(t *testing.T) {
	mail, sender, notice := suspensionMailFixture(adminconsole.ReasonOther)
	notice.Projects = []adminconsole.NoticeProject{
		{Name: "Shop\nPay at evil.test now", Apps: []string{"web\r\nClick evil.test", "plain", "tab\there\u2028split"}},
	}
	if err := mail.Suspended(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	body := sender.messages[0].TextBody
	want := `- Project "ShopPay at evil.test now": "webClick evil.test", "plain", "tabheresplit"`
	if !strings.Contains(body, want+"\n") {
		t.Fatalf("project line is not the cleaned, quoted one %q:\n%s", want, body)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "Pay at") || strings.HasPrefix(line, "Click") || strings.ContainsAny(line, "\r\u2028") {
			t.Errorf("injected line %q", line)
		}
	}
}

func TestSuspensionMail_NamesAreQuoted(t *testing.T) {
	mail, sender, notice := suspensionMailFixture(adminconsole.ReasonOther)
	notice.Projects = []adminconsole.NoticeProject{{Name: `say "hi"`, Apps: []string{`a"b`}}}
	if err := mail.Suspended(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	if want := `- Project "say \"hi\"": "a\"b"`; !strings.Contains(sender.messages[0].TextBody, want) {
		t.Fatalf("want %s in:\n%s", want, sender.messages[0].TextBody)
	}
}

func closureMessage(t *testing.T, cfg SuspensionMailConfig) string {
	t.Helper()
	sender := &sentMail{}
	cfg.From = "noreply@example.test"
	mail := NewSuspensionMail(sender, cfg)
	if err := mail.Suspended(context.Background(), adminconsole.SuspensionNotice{Email: "user@example.test", OrgClosed: true}); err != nil {
		t.Fatal(err)
	}
	return sender.messages[0].TextBody
}

// The retention comes from the same period the purge uses.
func TestClosureMail_NamesTheBackupRetentionWhenBackupsAreKept(t *testing.T) {
	body := closureMessage(t, SuspensionMailConfig{SupportEmail: "support@example.test", BackupsKept: true})
	days := int(RetainedBackupPeriod.Hours() / 24)
	for _, want := range []string{
		fmt.Sprintf("Backups of the databases are kept for %d days after that, then permanently removed.", days),
		fmt.Sprintf("write to support@example.test within those %d days", days),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("lacks %q:\n%s", want, body)
		}
	}
}

func TestClosureMail_SaysNoBackupsExistWhenNoTargetIsConfigured(t *testing.T) {
	body := closureMessage(t, SuspensionMailConfig{SupportEmail: "support@example.test"})
	if strings.Contains(body, "14 days") || strings.Contains(body, "Backups of the databases are kept") {
		t.Errorf("promises backups that do not exist:\n%s", body)
	}
	if !strings.Contains(body, "No backups of the databases exist") {
		t.Errorf("must say plainly that no backups exist:\n%s", body)
	}
}

// A name longer than the cap is cut at exactly the cap; one at the cap is whole.
func TestSuspensionMail_NamesAreCappedAtOneHundredCharacters(t *testing.T) {
	mail, sender, notice := suspensionMailFixture(adminconsole.ReasonOther)
	atCap := strings.Repeat("a", 100)
	overCap := strings.Repeat("b", 101)
	notice.Projects = []adminconsole.NoticeProject{{Name: atCap, Apps: []string{overCap}}}
	if err := mail.Suspended(context.Background(), notice); err != nil {
		t.Fatal(err)
	}
	body := sender.messages[0].TextBody
	if want := fmt.Sprintf("- Project %q: %q\n", atCap, strings.Repeat("b", 100)+"..."); !strings.Contains(body, want) {
		t.Fatalf("want %s in:\n%s", want, body)
	}
}
