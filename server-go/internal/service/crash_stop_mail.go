package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
)

// OrgMemberLister reads an organisation's members.
type OrgMemberLister interface {
	ListOrgMembers(ctx context.Context, orgID string) ([]*domain.OrgMember, error)
}

// CrashStopMail tells a project's organisation owners that an app was stopped
// for crashing (EXC-839). A failure is logged and never stops the guard.
type CrashStopMail struct {
	members   OrgMemberLister
	sender    email.Sender
	from      string
	fromName  string
	studioURL string
}

func NewCrashStopMail(members OrgMemberLister, sender email.Sender, from, fromName, studioURL string) *CrashStopMail {
	return &CrashStopMail{members: members, sender: sender, from: from, fromName: fromName, studioURL: strings.TrimRight(studioURL, "/")}
}

func (m *CrashStopMail) AppStopped(ctx context.Context, inst *domain.DatabaseInstance, app *apphost.App, suspension *apphost.Suspension) {
	ctx, cancel := context.WithTimeout(ctx, adminMailTimeout)
	defer cancel()
	members, err := m.members.ListOrgMembers(ctx, inst.OrgID)
	if err != nil {
		log.Printf("crash stop mail: list owners of org %s: %v", inst.OrgID, err)
		return
	}
	var owners []string
	for _, member := range members {
		if member.Role == "owner" && member.Email != "" {
			owners = append(owners, member.Email)
		}
	}
	if len(owners) == 0 {
		log.Printf("crash stop mail: org %s has no owner with an address", inst.OrgID)
		return
	}
	msg := email.Message{
		To: owners, From: m.from, FromName: m.fromName,
		Subject:  fmt.Sprintf("Your app %q was stopped: it kept crashing", app.Name),
		TextBody: m.body(inst, app, suspension),
		Tags:     map[string]string{"kind": "crash-loop-stop"},
	}
	if _, err := m.sender.Send(ctx, msg); err != nil {
		log.Printf("crash stop mail: send for app %s/%s: %v", app.ProjectID, app.ID, err)
	}
}

func (m *CrashStopMail) body(inst *domain.DatabaseInstance, app *apphost.App, suspension *apphost.Suspension) string {
	project := inst.ProjectName
	if project == "" {
		project = inst.ProjectID
	}
	text := fmt.Sprintf("Your app %q in project %q was stopped at %s: it kept crashing (%d restarts in the last hour).\n\n"+
		"It stays stopped until you start it again. Check its logs, fix the crash, then press Start again.\n",
		app.Name, project, time.Now().UTC().Format("2006-01-02 15:04 UTC"), suspension.Restarts)
	if m.studioURL != "" {
		text += fmt.Sprintf("\n%s/project/%s/containers/%s\n", m.studioURL, app.ProjectID, app.ID)
	}
	return text
}
