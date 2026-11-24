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
	"github.com/excalibase/provisioning-poc/internal/orgguard"
)

const (
	AuditActionSuspend     = "user.suspend"
	AuditActionUnsuspend   = "user.unsuspend"
	AuditActionProjectOpen = "admin.project.open"
	auditResourceUser      = "user"
	maxReasonLength        = 500
	platformRolePrefix     = "platform_"
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
	// MailSent is set once the suspension mail is settled: sent, or not owed.
	MailSent bool
}

// OwnedApp is an app in a project the account owns.
type OwnedApp struct {
	ProjectID string
	AppID     string
	Name      string
	Status    string
	Replicas  int
	// ProjectName is what the owner sees the project called.
	ProjectName string
}

// Suspension is the abuse_suspensions row.
type Suspension struct {
	UserID string
	Email  string
	Reason string
	Code   ReasonCode
	At     time.Time
}

// SuspendedApp is an app a suspension stopped and the replicas it ran, so an
// unsuspend restores exactly those and leaves an app the user paused alone.
type SuspendedApp struct {
	ProjectID        string `json:"projectId"`
	AppID            string `json:"appId"`
	AppName          string `json:"appName"`
	PreviousReplicas int    `json:"previousReplicas"`
	// ProjectName only feeds the account holder's mail; it is not stored.
	ProjectName string `json:"-"`
}

// SuspensionStore persists a suspension. Suspend and Unsuspend each change the
// account and its records in one transaction.
type SuspensionStore interface {
	FindUser(ctx context.Context, id string) (*UserRef, error)
	OwnedApps(ctx context.Context, userID string) ([]OwnedApp, error)
	// OwnedProjects lists the ids of the projects of the orgs the account owns, and those it created.
	OwnedProjects(ctx context.Context, userID string) ([]string, error)
	Suspend(ctx context.Context, suspension Suspension, apps []SuspendedApp) error
	// RecordSuspendedApps keeps the apps a suspension stopped, for the unsuspend to resume.
	RecordSuspendedApps(ctx context.Context, userID string, apps []SuspendedApp) error
	Unsuspend(ctx context.Context, userID string) error
	SuspendedApps(ctx context.Context, userID string) ([]SuspendedApp, error)
	// ClaimMail settles the suspension mail for one caller: true only for the
	// one that found it unsettled, so concurrent suspends send one mail.
	ClaimMail(ctx context.Context, userID string) (bool, error)
	// ReleaseMail gives the claim back after a send that failed, so a retry sends it.
	ReleaseMail(ctx context.Context, userID string) error
	ForgetSuspendedApp(ctx context.Context, userID, projectID, appID string) error
	// ClaimClosureMail settles the closure mail of one org for one caller: true
	// only for the one that found it unsent, so concurrent revokes send one mail.
	ClaimClosureMail(ctx context.Context, userID, orgID string) (bool, error)
	// ReleaseClosureMail gives the claim back after a send that failed.
	ReleaseClosureMail(ctx context.Context, userID, orgID string) error
	// MarkClosureMailSent records that the claimed mail went out.
	MarkClosureMailSent(ctx context.Context, userID, orgID string) error
	// ClosureMailSent says whether the claimed mail went out; false for a
	// claim still being sent.
	ClosureMailSent(ctx context.Context, userID, orgID string) (bool, error)
}

// AppControl stops and restarts an app through the app lifecycle, which scales
// the workload and records the replicas it ran.
type AppControl interface {
	// StopApp pauses an app, including one still rolling out.
	StopApp(ctx context.Context, projectID, appID string) (*apphost.App, error)
	ResumeApp(ctx context.Context, projectID, appID, actor string) (*apphost.App, error)
}

// ServingStep stops one thing a suspended account's project still serves
// besides its apps (sites, functions, public database ports), and brings it
// back on an unsuspend. A step must be safe to repeat.
type ServingStep interface {
	Name() string
	Stop(ctx context.Context, projectID string) error
	Restore(ctx context.Context, projectID string) error
}

// ServingFailure names a step that could not stop or restore a project, and why.
type ServingFailure struct {
	ProjectID string `json:"projectId"`
	Step      string `json:"step"`
	Reason    string `json:"reason"`
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
	// ServingFailed lists what could not be stopped or restored beyond the apps (EXC-845).
	ServingFailed []ServingFailure `json:"servingFailed,omitempty"`
}

// SuspensionService suspends and unsuspends accounts.
type SuspensionService struct {
	store SuspensionStore
	apps  AppControl
	audit AuditWriter
	now   func() time.Time

	steps       []ServingStep
	notifier    SuspensionNotifier
	mailEnabled func(context.Context) bool
}

func NewSuspensionService(store SuspensionStore, apps AppControl, audit AuditWriter) *SuspensionService {
	return &SuspensionService{store: store, apps: apps, audit: audit, now: time.Now}
}

// Suspend blocks the account, writes the suspension, stops the apps it runs
// and, while the mail flag is on, tells the account holder which rule it broke. The block comes first and stands whatever happens to the apps: an app
// that will not stop is reported, never a reason to leave the account usable.
func (s *SuspensionService) Suspend(ctx context.Context, actor Actor, userID string, why Reason) (*Result, error) {
	reason, err := cleanReason(why.Text)
	if err != nil {
		return nil, err
	}
	code, err := ParseReasonCode(string(why.Code))
	if err != nil {
		return nil, err
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
	// The suspension is written first: it is what refuses the org's other
	// members, so an app created while the list is read is refused too. A
	// failure after it leaves the account suspended; suspending again retries.
	suspension := Suspension{UserID: userID, Email: user.Email, Reason: reason, Code: code, At: s.now().UTC()}
	if err := s.store.Suspend(ctx, suspension, nil); err != nil {
		return nil, fmt.Errorf("suspend account: %w", err)
	}
	owned, err := s.store.OwnedApps(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list the account's apps: %w", err)
	}
	toStop := appsThatMayRun(owned)
	if err := s.store.RecordSuspendedApps(ctx, userID, toStop); err != nil {
		return nil, fmt.Errorf("record the apps to stop: %w", err)
	}
	s.record(ctx, actor, AuditActionSuspend, userID, reason)

	result := &Result{UserID: userID, Status: StatusSuspended, AppsStopped: []SuspendedApp{}, AppsRestored: []SuspendedApp{}, AppsFailed: []AppFailure{}}
	for _, app := range toStop {
		if _, err := s.apps.StopApp(ctx, app.ProjectID, app.AppID); err != nil {
			result.AppsFailed = append(result.AppsFailed, failure(app, err))
			continue
		}
		result.AppsStopped = append(result.AppsStopped, app)
	}
	result.ServingFailed = s.runSteps(ctx, userID, "stop", func(step ServingStep, projectID string) error {
		return step.Stop(ctx, projectID)
	})
	s.notify(ctx, user, why, code, result.AppsFailed)
	return result, nil
}

// AddServingSteps wires what a suspension stops besides apps, and an unsuspend restores.
func (s *SuspensionService) AddServingSteps(steps ...ServingStep) {
	s.steps = append(s.steps, steps...)
}

// runSteps applies a step to every project the account owns. Each failure is
// logged and reported, never a reason to stop the rest or undo the suspension.
func (s *SuspensionService) runSteps(ctx context.Context, userID, verb string, apply func(ServingStep, string) error) []ServingFailure {
	if len(s.steps) == 0 {
		return nil
	}
	projects, err := s.store.OwnedProjects(ctx, userID)
	if err != nil {
		log.Printf("ERROR: suspension %s: list the projects of %s: %v", verb, userID, err)
		return []ServingFailure{{Step: "list projects", Reason: err.Error()}}
	}
	var failed []ServingFailure
	for _, projectID := range projects {
		for _, step := range s.steps {
			if err := apply(step, projectID); err != nil {
				log.Printf("ERROR: suspension %s %s of %s: %v", verb, step.Name(), projectID, err)
				failed = append(failed, ServingFailure{ProjectID: projectID, Step: step.Name(), Reason: err.Error()})
			}
		}
	}
	return failed
}

// Unsuspend reactivates the account and resumes the apps the suspension
// stopped. An app that cannot resume keeps its record, so unsuspending again retries it.
func (s *SuspensionService) Unsuspend(ctx context.Context, actor Actor, userID, reason string) (*Result, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
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
	s.record(ctx, actor, AuditActionUnsuspend, userID, reason)

	stopped, err := s.store.SuspendedApps(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("list the apps the suspension stopped: %w", err)
	}
	result := &Result{UserID: userID, Status: StatusActive, AppsStopped: []SuspendedApp{}, AppsRestored: []SuspendedApp{}, AppsFailed: []AppFailure{}}
	// The admin lifting a suspension may serve the account's projects at once,
	// before the guard's short cache has seen the unsuspend.
	restoreCtx := orgguard.WithPlatformOverride(ctx)
	result.ServingFailed = s.runSteps(restoreCtx, userID, "restore", func(step ServingStep, projectID string) error {
		return step.Restore(restoreCtx, projectID)
	})
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

// cleanReason trims the written reason every admin action carries and refuses
// a blank or oversized one.
func cleanReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", ErrReasonRequired
	}
	if len(reason) > maxReasonLength {
		return "", ErrReasonTooLong
	}
	return reason, nil
}

// appsThatMayRun picks the apps a pause would stop: the ones with pods, which
// is also what a later resume brings back. An app whose deploy or resume is
// still rolling out holds pods before it reads ACTIVE, and would converge to
// running after the suspension if it were left (EXC-845).
func appsThatMayRun(owned []OwnedApp) []SuspendedApp {
	out := []SuspendedApp{}
	for _, app := range owned {
		switch app.Status {
		case apphost.StatusRunning, apphost.StatusFailed, apphost.StatusPausing, apphost.StatusCreated, apphost.StatusResuming:
			out = append(out, SuspendedApp{ProjectID: app.ProjectID, AppID: app.AppID, AppName: app.Name, PreviousReplicas: app.Replicas, ProjectName: app.ProjectName})
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
