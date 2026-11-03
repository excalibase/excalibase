package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/apptemplate"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
)

// OperationAppTemplate names the lease one template deploy holds on its project.
const OperationAppTemplate ProjectOperation = "template deploy"

var (
	ErrTemplateNotFound        = errors.New("template not found")
	ErrTemplateProjectNotFound = errors.New("project not found")
	// ErrTemplateRefused: the project cannot take the template now; nothing was created.
	ErrTemplateRefused = errors.New("the template cannot be deployed into this project")
	// ErrTemplateNetworkNeedsAdmin: only an org admin or owner opens a project's private network.
	ErrTemplateNetworkNeedsAdmin = errors.New("this template turns on the project's private network, which only an organisation admin or owner may do")
	// ErrTemplateNetworkUnconfirmed: the caller has to say yes to the private network first.
	ErrTemplateNetworkUnconfirmed = errors.New("this template turns on the project's private network; confirm it with confirmPrivateNetwork")
)

// TemplateRefusedError lists every reason the project cannot take the template.
type TemplateRefusedError struct{ Reasons []string }

func (e *TemplateRefusedError) Error() string {
	return ErrTemplateRefused.Error() + ": " + strings.Join(e.Reasons, "; ")
}

func (e *TemplateRefusedError) Unwrap() error { return ErrTemplateRefused }

// TemplateFailedError: a step failed after something was created, and all of
// it was removed again. Step is what a caller may read; Cause is for the log.
// ServerFault marks a failure of the platform rather than a state of the project.
type TemplateFailedError struct {
	Step        string
	Cause       error
	ServerFault bool
}

func (e *TemplateFailedError) Error() string {
	if strings.HasSuffix(e.Step, e.Cause.Error()) {
		return e.Public()
	}
	return e.Public() + ": " + e.Cause.Error()
}

// Public is the refusal a caller reads.
func (e *TemplateFailedError) Public() string {
	return "the template was not deployed and everything it created was removed: " + e.Step
}

func (e *TemplateFailedError) Unwrap() error { return e.Cause }

// TemplateRollbackError: the rollback could not remove everything; Remaining says what is left.
type TemplateRollbackError struct {
	Cause     error
	Remaining []string
}

func (e *TemplateRollbackError) Error() string {
	return fmt.Sprintf("the template was not deployed (%v) and could not remove %s; delete them and deploy again",
		e.Cause, strings.Join(e.Remaining, ", "))
}

func (e *TemplateRollbackError) Unwrap() error { return e.Cause }

// TemplateAppStore is the slice of the app store a template deploy writes through.
type TemplateAppStore interface {
	Create(app *apphost.App, maxApps int) error
	List(projectID string) ([]*apphost.App, error)
	RecordLifecycleFailure(projectID, id string, failure *apphost.LifecycleFailure) error
}

// TemplateAppDeployer is the slice of AppDeployService a template deploy drives.
type TemplateAppDeployer interface {
	DeployApp(ctx context.Context, projectID, appID, actor string) (*apphost.Deploy, error)
	DeleteApp(ctx context.Context, projectID, appID string, confirmDeleteDisk bool) error
	AdmitNewApps(ctx context.Context, projectID string, replicas []int) error
}

// TemplateNetwork is the project's private network between apps (EXC-524).
type TemplateNetwork interface {
	Describe(ctx context.Context, projectID string) (AppNetworkView, error)
	Set(ctx context.Context, projectID string, enabled bool) (AppNetworkView, error)
}

// TemplateSecretStore keeps generated values in the vault, under each app's own prefix.
type TemplateSecretStore interface {
	Put(path string, data map[string]string) error
	AppSecretPurger
}

// AppTemplateDeps: Network nil means the platform has no private network; Secrets nil, no vault.
type AppTemplateDeps struct {
	Apps     TemplateAppStore
	Deployer TemplateAppDeployer
	Projects AppNetworkProjectFinder
	Plans    PlanTiers
	Limits   apphost.AppLimits
	Disks    apphost.DiskLimits
	Budget   *storagebudget.Budget
	Network  TemplateNetwork
	Secrets  TemplateSecretStore
	Claimer  ProjectOperationClaimer
	// Flags show the catalog (EXC-669) beside the starters; nil keeps it hidden.
	Flags features.Flags
}

// AppTemplateService deploys a template (EXC-526): every plan limit is checked
// before anything is made, and a failure before the apps exist removes what
// the deploy made. The apps' first deploys run after the answer (EXC-669): a
// disk to prepare takes longer than a request should wait.
type AppTemplateService struct {
	catalog *apptemplate.Catalog
	deps    AppTemplateDeps
	claimer ProjectOperationClaimer
	secret  func(n int) (string, error)
	newID   func() string
	// async runs the first deploys after the answer; tests run them inline.
	async func(func())
	// leaseRetry spaces the rollback's retries of a delete another operation briefly holds.
	leaseRetry time.Duration
}

// rollbackDeleteAttempts bounds how long a rollback waits for an app's lease (about 30 s).
const rollbackDeleteAttempts = 60

func NewAppTemplateService(catalog *apptemplate.Catalog, deps AppTemplateDeps) *AppTemplateService {
	return &AppTemplateService{
		catalog: catalog, deps: deps, claimer: claimerOrInProcess(deps.Claimer),
		secret: apptemplate.GenerateSecret, newID: uuid.NewString, leaseRetry: 500 * time.Millisecond,
		async: func(run func()) { go run() },
	}
}

// TemplateDeployOptions: MayChangePrivateNetwork is the caller's role (admin or owner), decided by the handler.
type TemplateDeployOptions struct {
	ConfirmPrivateNetwork   bool
	MayChangePrivateNetwork bool
}

// TemplateDeployedApp is an app the deploy created; its first deploy starts after the answer.
type TemplateDeployedApp struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type TemplateDeployResult struct {
	TemplateID             string                `json:"templateId"`
	Apps                   []TemplateDeployedApp `json:"apps"`
	PrivateNetworkTurnedOn bool                  `json:"privateNetworkTurnedOn"`
	// Credentials are the template's showOnce values; this answer is the only time they are readable.
	Credentials apptemplate.ShownValues `json:"credentials,omitempty"`
	// AcceptedAt tells a failure the apps record afterwards from an older one.
	AcceptedAt time.Time `json:"acceptedAt"`
}

// template is a template this caller may see: a catalog one only while its flag is on.
func (s *AppTemplateService) template(ctx context.Context, templateID string) (*apptemplate.Template, error) {
	tpl, ok := s.catalog.Get(templateID)
	if !ok || !s.visible(ctx, tpl) {
		return nil, ErrTemplateNotFound
	}
	return tpl, nil
}

func (s *AppTemplateService) visible(ctx context.Context, tpl *apptemplate.Template) bool {
	return !tpl.Catalog || features.Enabled(ctx, s.deps.Flags, features.TemplateCatalog)
}

func (s *AppTemplateService) Deploy(ctx context.Context, projectID, templateID, actor string, opts TemplateDeployOptions) (*TemplateDeployResult, error) {
	tpl, err := s.template(ctx, templateID)
	if err != nil {
		return nil, err
	}
	ctx = context.WithoutCancel(ctx)
	release, claimed, err := s.claimer.Claim(ctx, "app-template:"+projectID, OperationAppTemplate)
	if err != nil {
		return nil, fmt.Errorf("claim project for %s: %w", OperationAppTemplate, err)
	}
	if !claimed {
		return nil, fmt.Errorf("%w (%s)", ErrProjectOperationRunning, projectID)
	}
	built, result, err := s.create(ctx, projectID, tpl, opts)
	if err != nil {
		release()
		return nil, err
	}
	// The project stays held until every first deploy has started.
	s.async(func() {
		defer release()
		s.deployCreated(ctx, built.Apps, actor)
	})
	return result, nil
}

// create checks the template against the project and makes its network,
// secrets and apps, all or nothing.
func (s *AppTemplateService) create(ctx context.Context, projectID string, tpl *apptemplate.Template,
	opts TemplateDeployOptions) (*apptemplate.Built, *TemplateDeployResult, error) {
	check, err := s.assess(ctx, projectID, tpl, opts.MayChangePrivateNetwork)
	if err != nil {
		return nil, nil, err
	}
	turnOn, err := s.admit(ctx, projectID, tpl, check, opts)
	if err != nil {
		return nil, nil, err
	}
	built, err := tpl.Build(apptemplate.BuildInput{
		ProjectID: projectID, Tier: check.fit.Plan, DatabaseName: check.databaseName,
		NewID: s.newID, Secret: s.secret,
	})
	if errors.Is(err, apptemplate.ErrSecretGeneration) {
		return nil, nil, err
	}
	if err != nil {
		return nil, nil, &TemplateRefusedError{Reasons: []string{err.Error()}}
	}
	result, err := s.apply(ctx, tpl, built, check.fit.AppsAllowed, turnOn)
	if err != nil {
		return nil, nil, err
	}
	return built, result, nil
}

// admit runs every check that needs no write: the plan's limits, the private
// network rule, room in the cluster and the storage budget.
func (s *AppTemplateService) admit(ctx context.Context, projectID string, tpl *apptemplate.Template,
	check *templateCheck, opts TemplateDeployOptions) (bool, error) {
	if len(check.fit.Refusals) > 0 {
		return false, &TemplateRefusedError{Reasons: check.fit.Refusals}
	}
	turnOn := tpl.Facts().NeedsPrivateNetwork && !check.fit.PrivateNetworkOn
	if turnOn && !opts.MayChangePrivateNetwork {
		return false, ErrTemplateNetworkNeedsAdmin
	}
	if turnOn && !opts.ConfirmPrivateNetwork {
		return false, ErrTemplateNetworkUnconfirmed
	}
	replicas := make([]int, 0, len(tpl.Apps))
	for _, app := range tpl.Apps {
		replicas = append(replicas, *app.Replicas)
	}
	if err := s.deps.Deployer.AdmitNewApps(ctx, projectID, replicas); err != nil {
		if errors.Is(err, ErrAppCapacity) || errors.Is(err, ErrAppNoSandboxNode) || errors.Is(err, ErrAppOverPlan) {
			return false, &TemplateRefusedError{Reasons: []string{err.Error()}}
		}
		return false, err
	}
	if disk := tpl.Facts().DiskBytes; disk > 0 {
		if err := s.deps.Budget.Check(ctx, disk, "the template's disks"); err != nil {
			if errors.Is(err, storagebudget.ErrExceeded) {
				return false, &TemplateRefusedError{Reasons: []string{err.Error()}}
			}
			return false, fmt.Errorf("read the platform's storage budget: %w", err)
		}
	}
	return turnOn, nil
}

// closeNetwork waits out another operation holding the project, as deleteCreated does.
func (s *AppTemplateService) closeNetwork(ctx context.Context, projectID string) error {
	var err error
	for range rollbackDeleteAttempts {
		_, err = s.deps.Network.Set(ctx, projectID, false)
		if !errors.Is(err, ErrProjectOperationRunning) {
			return err
		}
		time.Sleep(s.leaseRetry)
	}
	return err
}

// deleteCreated retries while the app's lease is held: the rollout watch of
// the deploy just made takes it for a moment when the rollout ends.
func (s *AppTemplateService) deleteCreated(ctx context.Context, app *apphost.App) error {
	var err error
	for range rollbackDeleteAttempts {
		err = s.deps.Deployer.DeleteApp(ctx, app.ProjectID, app.ID, true)
		if !errors.Is(err, ErrProjectOperationRunning) {
			return err
		}
		time.Sleep(s.leaseRetry)
	}
	return err
}

// templateJournal is what a deploy made, undone in reverse.
type templateJournal struct {
	projectID string
	networkOn bool
	created   []*apphost.App
	secretsOf []*apphost.App
}

func (s *AppTemplateService) apply(ctx context.Context, tpl *apptemplate.Template, built *apptemplate.Built,
	maxApps int, turnOn bool) (*TemplateDeployResult, error) {
	projectID := built.Apps[0].ProjectID
	journal := templateJournal{projectID: projectID}
	if turnOn {
		// Journaled first: an error after the cluster opened it must still close it.
		journal.networkOn = true
		if _, err := s.deps.Network.Set(ctx, projectID, true); err != nil {
			return nil, s.rollback(ctx, &journal, serverFault("could not turn on the project's private network", err))
		}
	}
	for _, app := range built.Apps {
		if err := s.storeSecrets(app, built.Secrets, &journal); err != nil {
			return nil, s.rollback(ctx, &journal, serverFault(fmt.Sprintf("could not store the secrets of app %q", app.Name), err))
		}
		if err := s.deps.Apps.Create(app, maxApps); err != nil {
			return nil, s.rollback(ctx, &journal, createFailure(app, err))
		}
		journal.created = append(journal.created, app)
	}
	result := &TemplateDeployResult{TemplateID: tpl.ID, PrivateNetworkTurnedOn: turnOn, Credentials: built.Shown,
		AcceptedAt: time.Now().UTC()}
	for _, app := range built.Apps {
		result.Apps = append(result.Apps, TemplateDeployedApp{ID: app.ID, Name: app.Name})
	}
	return result, nil
}

// deployCreated starts each app's first deploy. A deploy that fails is the
// app's failed deploy; one that could not even start is written on the app,
// so Studio shows it either way. Nothing is removed: the apps are the
// developer's now, to read, fix and deploy again.
func (s *AppTemplateService) deployCreated(ctx context.Context, apps []*apphost.App, actor string) {
	for _, app := range apps {
		deploy, err := s.deps.Deployer.DeployApp(ctx, app.ProjectID, app.ID, actor)
		if err == nil {
			if deploy.Status == apphost.DeployStatusFailed {
				log.Printf("template deploy: app %s/%s: first deploy failed: %s", app.ProjectID, app.ID, deploy.FailureReason)
			}
			continue
		}
		log.Printf("template deploy: app %s/%s: first deploy did not start: %v", app.ProjectID, app.ID, err)
		failure := &apphost.LifecycleFailure{Operation: string(OperationAppTemplate),
			Reason: "its first deploy did not start; deploy the app again", At: time.Now().UTC()}
		if recordErr := s.deps.Apps.RecordLifecycleFailure(app.ProjectID, app.ID, failure); recordErr != nil {
			log.Printf("template deploy: app %s/%s: record the failure: %v", app.ProjectID, app.ID, recordErr)
		}
	}
}

func serverFault(step string, cause error) *TemplateFailedError {
	return &TemplateFailedError{Step: step, Cause: cause, ServerFault: true}
}

// createFailure: a name or a slot taken meanwhile is the project's state; anything else is ours.
func createFailure(app *apphost.App, err error) *TemplateFailedError {
	if errors.Is(err, apphost.ErrAppNameTaken) || errors.Is(err, apphost.ErrAppLimitReached) {
		return &TemplateFailedError{Step: fmt.Sprintf("create app %q: %v", app.Name, err), Cause: err}
	}
	return serverFault(fmt.Sprintf("could not create app %q", app.Name), err)
}

// storeSecrets writes the app's generated values before the app exists, so a
// variable never points at a value that was not stored.
func (s *AppTemplateService) storeSecrets(app *apphost.App, secrets apptemplate.SecretValues, journal *templateJournal) error {
	prefix := apphost.AppSecretPrefix(app.ProjectID, app.ID)
	written := false
	for path, value := range secrets {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		if !written {
			journal.secretsOf = append(journal.secretsOf, app)
			written = true
		}
		if err := s.deps.Secrets.Put(path, map[string]string{apphost.AppSecretValueKey: value}); err != nil {
			return err
		}
	}
	return nil
}

// rollback deletes the created apps (their disks, workloads and secrets with
// them), purges secrets of apps never created, and closes the network last,
// and only if nothing it opened for is left.
func (s *AppTemplateService) rollback(ctx context.Context, journal *templateJournal, failure *TemplateFailedError) error {
	var remaining []string
	created := map[string]bool{}
	for i := len(journal.created) - 1; i >= 0; i-- {
		app := journal.created[i]
		created[app.ID] = true
		if err := s.deleteCreated(ctx, app); err != nil {
			log.Printf("template rollback: delete app %s: %v", app.ID, err)
			remaining = append(remaining, fmt.Sprintf("app %q", app.Name))
		}
	}
	for _, app := range journal.secretsOf {
		if created[app.ID] {
			continue
		}
		if _, err := s.deps.Secrets.DeletePrefix(apphost.AppSecretPrefix(app.ProjectID, app.ID)); err != nil {
			log.Printf("template rollback: purge secrets of %s: %v", app.ID, err)
			remaining = append(remaining, fmt.Sprintf("the stored secrets of %q", app.Name))
		}
	}
	if journal.networkOn && len(remaining) == 0 {
		if err := s.closeNetwork(ctx, journal.projectID); err != nil {
			log.Printf("template rollback: close the private network: %v", err)
			remaining = append(remaining, "the private network it turned on")
		}
	} else if journal.networkOn {
		remaining = append(remaining, "the private network it turned on")
	}
	if len(remaining) > 0 {
		return &TemplateRollbackError{Cause: failure, Remaining: remaining}
	}
	return failure
}
