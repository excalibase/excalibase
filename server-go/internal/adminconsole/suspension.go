// Package adminconsole is the platform admin's view of every account, org and
// project, and the one operation that acts on abuse: suspending an account
// together with everything it runs (EXC-757).
package adminconsole

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

const (
	AuditActionSuspend   = "user.suspend"
	AuditActionUnsuspend = "user.unsuspend"
	auditResourceUser    = "user"
	maxReasonLength      = 500
	platformRolePrefix   = "platform_"
)

var (
	ErrUserNotFound     = errors.New("user not found")
	ErrReasonRequired   = errors.New("a reason is required to suspend an account")
	ErrReasonTooLong    = errors.New("the reason is too long")
	ErrProtectedAccount = errors.New("platform accounts and your own account cannot be suspended")
)

// Actor is the admin acting, for the audit entry.
type Actor struct {
	ID string
	IP string
}

// UserRef is what suspension needs to know about an account.
type UserRef struct {
	ID        string
	Email     string
	Role      string
	Active    bool
	Suspended bool
}

// OwnedApp is an app in a project the account owns.
type OwnedApp struct {
	ProjectID string
	AppID     string
	Name      string
	Status    string
	Replicas  int
}

// Suspension is the abuse_suspensions row.
type Suspension struct {
	UserID string
	Email  string
	Reason string
	At     time.Time
}

// SuspendedApp is an app a suspension stopped and the replicas it ran, so an
// unsuspend restores exactly those and leaves an app the user paused alone.
type SuspendedApp struct {
	ProjectID        string `json:"projectId"`
	AppID            string `json:"appId"`
	AppName          string `json:"appName"`
	PreviousReplicas int    `json:"previousReplicas"`
}

// SuspensionStore persists a suspension. Suspend and Unsuspend each change the
// account and its records in one transaction.
type SuspensionStore interface {
	FindUser(ctx context.Context, id string) (*UserRef, error)
	OwnedApps(ctx context.Context, userID string) ([]OwnedApp, error)
	Suspend(ctx context.Context, suspension Suspension, apps []SuspendedApp) error
	Unsuspend(ctx context.Context, userID string) error
	SuspendedApps(ctx context.Context, userID string) ([]SuspendedApp, error)
	ForgetSuspendedApp(ctx context.Context, userID, projectID, appID string) error
}

// AppControl stops and restarts an app through the app lifecycle, which scales
// the workload and records the replicas it ran.
type AppControl interface {
	PauseApp(ctx context.Context, projectID, appID string) (*apphost.App, error)
	ResumeApp(ctx context.Context, projectID, appID, actor string) (*apphost.App, error)
}

// AuditWriter records the admin's action.
type AuditWriter interface {
	LogAudit(ctx context.Context, entry *domain.AuditEntry) error
}

// AppFailure names an app an operation could not move, and why.
type AppFailure struct {
	ProjectID string `json:"projectId"`
	AppID     string `json:"appId"`
	AppName   string `json:"appName"`
	Reason    string `json:"reason"`
}

// Result says what a suspend or unsuspend did to the account's apps.
type Result struct {
	UserID       string         `json:"userId"`
	Status       string         `json:"status"`
	AppsStopped  []SuspendedApp `json:"appsStopped"`
	AppsRestored []SuspendedApp `json:"appsRestored"`
	AppsFailed   []AppFailure   `json:"appsFailed"`
}

// SuspensionService suspends and unsuspends accounts.
type SuspensionService struct {
	store SuspensionStore
	apps  AppControl
	audit AuditWriter
	now   func() time.Time
}

func NewSuspensionService(store SuspensionStore, apps AppControl, audit AuditWriter) *SuspensionService {
	return &SuspensionService{store: store, apps: apps, audit: audit, now: time.Now}
}

// Suspend blocks the account, writes the suspension and stops the apps it
// runs. The block comes first and stands whatever happens to the apps: an app
// that will not stop is reported, never a reason to leave the account usable.
func (s *SuspensionService) Suspend(ctx context.Context, actor Actor, userID, reason string) (*Result, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, ErrReasonRequired
	}
	if len(reason) > maxReasonLength {
		return nil, ErrReasonTooLong
	}
	user, err := s.store.FindUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("look up user: %w", err)
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
	if user.ID == actor.ID || strings.HasPrefix(user.Role, platformRolePrefix) {
		return nil, ErrProtectedAccount
	}
	owned, err := s.store.OwnedApps(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list the account's apps: %w", err)
	}
	toStop := appsThatMayRun(owned)
	suspension := Suspension{UserID: userID, Email: user.Email, Reason: reason, At: s.now().UTC()}
	if err := s.store.Suspend(ctx, suspension, toStop); err != nil {
		return nil, fmt.Errorf("suspend account: %w", err)
	}
	s.record(ctx, actor, AuditActionSuspend, userID, reason)

	result := &Result{UserID: userID, Status: StatusSuspended, AppsStopped: []SuspendedApp{}, AppsRestored: []SuspendedApp{}, AppsFailed: []AppFailure{}}
	for _, app := range toStop {
		if _, err := s.apps.PauseApp(ctx, app.ProjectID, app.AppID); err != nil {
			result.AppsFailed = append(result.AppsFailed, failure(app, err))
			continue
		}
		result.AppsStopped = append(result.AppsStopped, app)
	}
	return result, nil
}

// Unsuspend reactivates the account and resumes the apps the suspension
// stopped. An app that cannot resume keeps its record, so unsuspending again retries it.
func (s *SuspensionService) Unsuspend(ctx context.Context, actor Actor, userID string) (*Result, error) {
	user, err := s.store.FindUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("look up user: %w", err)
	}
	if user == nil {
		return nil, ErrUserNotFound
	}
	if err := s.store.Unsuspend(ctx, userID); err != nil {
		return nil, fmt.Errorf("unsuspend account: %w", err)
	}
	s.record(ctx, actor, AuditActionUnsuspend, userID, "")

	stopped, err := s.store.SuspendedApps(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list the apps the suspension stopped: %w", err)
	}
	result := &Result{UserID: userID, Status: StatusActive, AppsStopped: []SuspendedApp{}, AppsRestored: []SuspendedApp{}, AppsFailed: []AppFailure{}}
	for _, app := range stopped {
		if _, err := s.apps.ResumeApp(ctx, app.ProjectID, app.AppID, actor.ID); err != nil {
			result.AppsFailed = append(result.AppsFailed, failure(app, err))
			continue
		}
		if err := s.store.ForgetSuspendedApp(ctx, userID, app.ProjectID, app.AppID); err != nil {
			return nil, fmt.Errorf("forget restored app: %w", err)
		}
		result.AppsRestored = append(result.AppsRestored, app)
	}
	return result, nil
}

// appsThatMayRun picks the apps a pause would stop: the ones with pods, which
// is also what a later resume brings back.
func appsThatMayRun(owned []OwnedApp) []SuspendedApp {
	out := []SuspendedApp{}
	for _, app := range owned {
		if app.Status == apphost.StatusRunning || app.Status == apphost.StatusFailed || app.Status == apphost.StatusPausing {
			out = append(out, SuspendedApp{ProjectID: app.ProjectID, AppID: app.AppID, AppName: app.Name, PreviousReplicas: app.Replicas})
		}
	}
	return out
}

func failure(app SuspendedApp, err error) AppFailure {
	return AppFailure{ProjectID: app.ProjectID, AppID: app.AppID, AppName: app.AppName, Reason: err.Error()}
}

// record audits the action; a failed audit write is logged, never a reason to
// undo a suspension the admin already made.
func (s *SuspensionService) record(ctx context.Context, actor Actor, action, userID, details string) {
	entry := &domain.AuditEntry{UserID: actor.ID, Action: action, Resource: auditResourceUser, ResourceID: userID, Details: details, IPAddress: actor.IP}
	if err := s.audit.LogAudit(ctx, entry); err != nil {
		log.Printf("ERROR: audit %s %s: %v", action, userID, err)
	}
}
