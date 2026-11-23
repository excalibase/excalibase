package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
)

type memberList struct {
	members []*domain.OrgMember
	err     error
}

func (m memberList) ListOrgMembers(context.Context, string) ([]*domain.OrgMember, error) {
	return m.members, m.err
}

type sentMail struct {
	messages []email.Message
	err      error
}

func (s *sentMail) Send(_ context.Context, msg email.Message) (string, error) {
	s.messages = append(s.messages, msg)
	return "id", s.err
}

func stoppedFixture() (*domain.DatabaseInstance, *apphost.App, *apphost.Suspension) {
	return &domain.DatabaseInstance{ProjectID: "proj-1", OrgID: "org-1", ProjectName: "Shop"},
		&apphost.App{ID: "app-1", Name: "web", ProjectID: "proj-1"},
		&apphost.Suspension{Kind: apphost.SuspensionCrashLoop, Reason: "kept crashing (11 restarts in the last hour)", Restarts: 11}
}

func TestCrashStopMail_TellsOnlyTheOrgOwners(t *testing.T) {
	sender := &sentMail{}
	mail := NewCrashStopMail(memberList{members: []*domain.OrgMember{
		{Role: "owner", Email: "owner@example.test"},
		{Role: "developer", Email: "dev@example.test"},
		{Role: "owner"},
	}}, sender, "noreply@example.test", "Excalibase", "https://studio.example.test")
	inst, app, suspension := stoppedFixture()

	mail.AppStopped(context.Background(), inst, app, suspension)

	if len(sender.messages) != 1 || len(sender.messages[0].To) != 1 || sender.messages[0].To[0] != "owner@example.test" {
		t.Fatalf("messages = %+v, want one mail to the owner only", sender.messages)
	}
	msg := sender.messages[0]
	for _, want := range []string{"web", "11 restarts", "https://studio.example.test/project/proj-1/containers/app-1", "Start again"} {
		if !strings.Contains(msg.TextBody, want) {
			t.Errorf("mail lacks %q: %s", want, msg.TextBody)
		}
	}
	if !strings.Contains(msg.Subject, "web") {
		t.Errorf("subject = %q", msg.Subject)
	}
}

func TestCrashStopMail_FailuresNeverEscape(t *testing.T) {
	inst, app, suspension := stoppedFixture()
	NewCrashStopMail(memberList{err: errors.New("db down")}, &sentMail{}, "a@b.test", "", "").AppStopped(context.Background(), inst, app, suspension)
	sender := &sentMail{err: errors.New("smtp down")}
	NewCrashStopMail(memberList{members: []*domain.OrgMember{{Role: "owner", Email: "o@example.test"}}}, sender, "a@b.test", "", "").
		AppStopped(context.Background(), inst, app, suspension)
	if len(sender.messages) != 1 {
		t.Fatalf("a mail that fails is still tried once: %d", len(sender.messages))
	}
}
