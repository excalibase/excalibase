package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// IdleWarnEmailConfig wires the owner-email warning.
type IdleWarnEmailConfig struct {
	Users        storage.UserStore
	Sender       email.Sender
	ProductName  string
	DashboardURL string
}

// IdleWarnEmail emails the project owner one day before an idle project is
// auto-paused. Satisfies IdleWarnNotifier.
type IdleWarnEmail struct {
	users        storage.UserStore
	sender       email.Sender
	productName  string
	dashboardURL string
}

func NewIdleWarnEmail(c IdleWarnEmailConfig) *IdleWarnEmail {
	return &IdleWarnEmail{users: c.Users, sender: c.Sender, productName: c.ProductName, dashboardURL: c.DashboardURL}
}

var errNoOwnerEmail = errors.New("project owner has no email address")

// NotifyIdleWarning resolves the owner and sends the warning. Returns
// email.ErrNotConfigured untouched when no provider is wired so the caller
// can treat it as "no channel" rather than a delivery failure.
func (n *IdleWarnEmail) NotifyIdleWarning(ctx context.Context, inst *domain.DatabaseInstance, pauseAt time.Time) error {
	if n.sender == nil || n.users == nil {
		return email.ErrNotConfigured
	}
	owner, err := n.users.FindUserByID(ctx, inst.OwnerID)
	if err != nil {
		return fmt.Errorf("lookup owner: %w", err)
	}
	if owner == nil || owner.Email == "" {
		return errNoOwnerEmail
	}
	msg, err := email.BuildAlertEmail(email.AlertData{
		Title:        "Project " + displayName(inst) + " will be paused for inactivity",
		Severity:     "medium",
		Body:         idleWarningBody(inst, pauseAt),
		ProjectID:    inst.ProjectID,
		DashboardURL: n.dashboardURL,
		ProductName:  n.productName,
	})
	if err != nil {
		return fmt.Errorf("build warning email: %w", err)
	}
	msg.To = []string{owner.Email}
	msg.Tags["category"] = "idle-pause-warning"
	_, err = n.sender.Send(ctx, msg)
	return err
}

func displayName(inst *domain.DatabaseInstance) string {
	if inst.ProjectName != "" {
		return inst.ProjectName
	}
	return inst.ProjectID
}

func idleWarningBody(inst *domain.DatabaseInstance, pauseAt time.Time) string {
	return fmt.Sprintf(
		"Your project %s (%s) has had no activity for a while and will be paused automatically on %s. "+
			"Any request to the project before then keeps it running; a paused project can be resumed from the dashboard at any time.",
		displayName(inst), inst.ProjectID, pauseAt.UTC().Format("Mon 2 Jan 2006 15:04 UTC"))
}
