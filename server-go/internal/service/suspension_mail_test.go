package service

import (
	"context"
	"errors"
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
