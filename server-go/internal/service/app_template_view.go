package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/apptemplate"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// TemplateFit is what a template costs in the project's plan, and why it cannot be deployed now, if it cannot.
type TemplateFit struct {
	Plan         domain.TierType `json:"plan"`
	AppsNeeded   int             `json:"appsNeeded"`
	AppsHeld     int             `json:"appsHeld"`
	AppsAllowed  int             `json:"appsAllowed"`
	DiskBytes    int64           `json:"diskBytes"`
	DiskCapBytes int64           `json:"diskCapBytes"`
	// AppCPU and AppMemory are what each copy of each app gets on this plan.
	AppCPU                  string   `json:"appCpu"`
	AppMemory               string   `json:"appMemory"`
	PrivateNetworkOn        bool     `json:"privateNetworkOn"`
	CanTurnOnPrivateNetwork bool     `json:"canTurnOnPrivateNetwork"`
	DatabaseReady           bool     `json:"databaseReady"`
	Refusals                []string `json:"refusals"`
}

// TemplateVarView names where a variable's value comes from; only a plain literal shows its value.
type TemplateVarView struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Value  string `json:"value,omitempty"`
}

type TemplateAppView struct {
	Name          string                `json:"name"`
	Image         string                `json:"image"`
	Internal      bool                  `json:"internal"`
	Port          int                   `json:"port,omitempty"`
	InternalPorts []int                 `json:"internalPorts,omitempty"`
	Replicas      int                   `json:"replicas"`
	Args          []string              `json:"args,omitempty"`
	Disk          *apptemplate.DiskSpec `json:"disk,omitempty"`
	Env           []TemplateVarView     `json:"env"`
}

type TemplateView struct {
	ID                  string            `json:"id"`
	Format              string            `json:"format"`
	Name                string            `json:"name"`
	Summary             string            `json:"summary"`
	Description         string            `json:"description,omitempty"`
	Apps                []TemplateAppView `json:"apps"`
	NeedsPrivateNetwork bool              `json:"needsPrivateNetwork"`
	NeedsDatabase       bool              `json:"needsDatabase"`
	Fit                 TemplateFit       `json:"fit"`
	Source              string            `json:"source,omitempty"`
}

// templateCheck is one reading of the project, shared by the view and the deploy.
type templateCheck struct {
	fit          TemplateFit
	databaseName string
}

// List shows every template with what it would cost this project.
func (s *AppTemplateService) List(ctx context.Context, projectID string, mayChangeNetwork bool) ([]TemplateView, error) {
	templates := s.catalog.List()
	views := make([]TemplateView, 0, len(templates))
	for _, tpl := range templates {
		check, err := s.assess(ctx, projectID, tpl, mayChangeNetwork)
		if err != nil {
			return nil, err
		}
		views = append(views, viewOf(tpl, check.fit))
	}
	return views, nil
}

// Get is one template with its source document.
func (s *AppTemplateService) Get(ctx context.Context, projectID, templateID string, mayChangeNetwork bool) (TemplateView, error) {
	tpl, ok := s.catalog.Get(templateID)
	if !ok {
		return TemplateView{}, ErrTemplateNotFound
	}
	check, err := s.assess(ctx, projectID, tpl, mayChangeNetwork)
	if err != nil {
		return TemplateView{}, err
	}
	view := viewOf(tpl, check.fit)
	view.Source = tpl.Source
	return view, nil
}

// assess reads the project and its plan once and lists every plan-level
// reason the template cannot go in. Room in the cluster is asked on deploy only.
func (s *AppTemplateService) assess(ctx context.Context, projectID string, tpl *apptemplate.Template, mayChangeNetwork bool) (*templateCheck, error) {
	facts := tpl.Facts()
	inst, err := s.deps.Projects.FindByProjectID(projectID)
	if err != nil {
		return nil, fmt.Errorf("read project %s: %w", projectID, err)
	}
	if inst == nil {
		return nil, ErrTemplateProjectNotFound
	}
	fit := TemplateFit{AppsNeeded: facts.Apps, DiskBytes: facts.DiskBytes, CanTurnOnPrivateNetwork: mayChangeNetwork, Refusals: []string{}}
	if err := s.readPlan(ctx, projectID, &fit); err != nil {
		return nil, err
	}
	held, err := s.deps.Apps.List(projectID)
	if err != nil {
		return nil, fmt.Errorf("list the project's apps: %w", err)
	}
	fit.AppsHeld = len(held)
	check := &templateCheck{fit: fit}
	check.databaseName = servingDatabase(inst)
	check.fit.DatabaseReady = check.databaseName != ""
	if err := s.refusals(ctx, projectID, tpl, facts, inst, held, check); err != nil {
		return nil, err
	}
	return check, nil
}

func (s *AppTemplateService) readPlan(ctx context.Context, projectID string, fit *TemplateFit) error {
	tier, err := s.deps.Plans.ProjectPlanTier(ctx, projectID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	size, err := config.GetAppTierConfig(tier)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	if s.deps.Limits == nil || s.deps.Disks == nil {
		return fmt.Errorf("%w: no source for the plan's app limits", ErrOrgTierUnresolved)
	}
	if fit.AppsAllowed, err = s.deps.Limits.MaxApps(ctx, projectID); err != nil {
		return fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	if fit.DiskCapBytes, err = s.deps.Disks.MaxDiskBytes(ctx, projectID); err != nil {
		return fmt.Errorf("%w: %v", ErrOrgTierUnresolved, err)
	}
	fit.Plan, fit.AppCPU, fit.AppMemory = tier, size.CPULimit, size.MemoryLimit
	return nil
}

func (s *AppTemplateService) refusals(ctx context.Context, projectID string, tpl *apptemplate.Template,
	facts apptemplate.Facts, inst *domain.DatabaseInstance, held []*apphost.App, check *templateCheck) error {
	fit := &check.fit
	refuse := func(format string, args ...any) { fit.Refusals = append(fit.Refusals, fmt.Sprintf(format, args...)) }
	if inst.DeploymentMode != domain.ModeK8s || inst.Namespace == "" {
		refuse("the project has nowhere to run apps yet")
	}
	if fit.AppsHeld+fit.AppsNeeded > fit.AppsAllowed {
		refuse("the template needs %d apps and the project holds %d of the %d its %s plan allows",
			fit.AppsNeeded, fit.AppsHeld, fit.AppsAllowed, fit.Plan)
	}
	names := map[string]bool{}
	for _, app := range held {
		names[app.Name] = true
	}
	for _, app := range tpl.Apps {
		if names[app.Name] {
			refuse("the project already has an app named %q", app.Name)
		}
		if app.Disk != nil {
			if err := apphost.CheckDiskWithinPlan(&apphost.AppDisk{MountPath: app.Disk.MountPath, Size: app.Disk.Size}, fit.DiskCapBytes); err != nil {
				refuse("app %q: %v", app.Name, err)
			}
		}
	}
	if facts.GeneratesSecrets && s.deps.Secrets == nil {
		refuse("the template generates secrets and no vault is configured to keep them")
	}
	if facts.NeedsDatabase && !fit.DatabaseReady {
		refuse("the template needs the project's database, and the project has none that is ready")
	}
	if !facts.NeedsPrivateNetwork {
		return nil
	}
	if s.deps.Network == nil {
		refuse("the template needs the project's private network, which this platform does not offer")
		return nil
	}
	view, err := s.deps.Network.Describe(ctx, projectID)
	if err != nil {
		if errors.Is(err, ErrAppNetworkUnsupported) {
			refuse("the template needs the project's private network: %v", err)
			return nil
		}
		return fmt.Errorf("read the project's private network: %w", err)
	}
	fit.PrivateNetworkOn = view.PrivateNetwork
	return nil
}

// servingDatabase is the project's database name while it serves, else "".
func servingDatabase(inst *domain.DatabaseInstance) string {
	if inst.NoDatabase || inst.DatabaseName == "" || domain.IsNotServable(inst.Status) || domain.IsBuildingStatus(inst.Status) {
		return ""
	}
	return inst.DatabaseName
}

func viewOf(tpl *apptemplate.Template, fit TemplateFit) TemplateView {
	facts := tpl.Facts()
	view := TemplateView{
		ID: tpl.ID, Format: tpl.Format, Name: tpl.Name, Summary: tpl.Summary, Description: tpl.Description,
		NeedsPrivateNetwork: facts.NeedsPrivateNetwork, NeedsDatabase: facts.NeedsDatabase, Fit: fit,
	}
	for _, app := range tpl.Apps {
		appView := TemplateAppView{
			Name: app.Name, Image: app.Image, Internal: app.Internal, Port: app.Port,
			InternalPorts: app.InternalPorts, Replicas: *app.Replicas, Args: app.Args, Disk: app.Disk,
			Env: make([]TemplateVarView, 0, len(app.Env)),
		}
		for _, v := range app.Env {
			source := apptemplate.ValueSource(v.Value)
			varView := TemplateVarView{Name: v.Name, Source: source}
			if source == apptemplate.SourceLiteral {
				varView.Value = v.Value
			}
			appView.Env = append(appView.Env, varView)
		}
		view.Apps = append(view.Apps, appView)
	}
	return view
}
