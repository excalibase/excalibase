// Package apptemplate is the Excalibase template format (EXC-526): a
// versioned document that declares a set of apps — images, ports, disks,
// arguments and variables — deployed into a project together.
//
// A variable's value is text with ${{ ... }} expressions in it:
//
//	${{ secret(32) }}                 a value generated on deploy (16-128 characters)
//	${{ db.DATABASE_URL }}            the project's database (the whole value only)
//	${{ apps.<name>.host }}           another app's in-project host name
//	${{ apps.<name>.port }}           its port: the first internal port, or 80 for a web app
//	${{ apps.<name>.env.<VAR> }}      the value another app's variable resolves to
//
// Parsing validates everything a template can decide on its own. What depends
// on the project — its plan, its database, its apps — is decided on deploy.
package apptemplate

import (
	"errors"
	"fmt"
	"regexp"

	"sigs.k8s.io/yaml"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Format is the only version this platform reads.
const Format = "excalibase.template/v1"

const (
	// MaxTemplateBytes bounds a template document before it is parsed.
	MaxTemplateBytes = 64 * 1024
	// MaxTemplateApps is the most apps any plan holds in one project.
	MaxTemplateApps  = 20
	maxNameLength    = 80
	maxSummaryLength = 200
	maxDescLength    = 4000
)

// ErrInvalidTemplate marks a document that is not a valid template.
var ErrInvalidTemplate = errors.New("invalid template")

var validTemplateID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,48}[a-z0-9]$`)

// Template is one parsed, validated template.
type Template struct {
	Format      string    `json:"format"`
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Summary     string    `json:"summary"`
	Description string    `json:"description,omitempty"`
	Apps        []AppSpec `json:"apps"`
	// Source is the document as written, shown on the details page.
	Source string `json:"-"`
}

// AppSpec is one app a template creates.
type AppSpec struct {
	Name            string    `json:"name"`
	Image           string    `json:"image"`
	Internal        bool      `json:"internal,omitempty"`
	Port            int       `json:"port,omitempty"`
	HealthCheckPath string    `json:"healthCheckPath,omitempty"`
	InternalPorts   []int     `json:"internalPorts,omitempty"`
	Replicas        *int      `json:"replicas"`
	Args            []string  `json:"args,omitempty"`
	Disk            *DiskSpec `json:"disk,omitempty"`
	Env             []EnvSpec `json:"env,omitempty"`
}

// DiskSpec is the app's one persistent disk.
type DiskSpec struct {
	MountPath string `json:"mountPath"`
	Size      string `json:"size"`
}

// EnvSpec is one variable; Value may hold ${{ ... }} expressions.
type EnvSpec struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidTemplate, fmt.Sprintf(format, args...))
}

// Parse reads a template document. Unknown fields are refused, so a template
// can never ask for something (a host path, a privilege) this format has no word for.
func Parse(source []byte) (*Template, error) {
	if len(source) > MaxTemplateBytes {
		return nil, invalid("the document exceeds %d bytes", MaxTemplateBytes)
	}
	var tpl Template
	if err := yaml.UnmarshalStrict(source, &tpl); err != nil {
		return nil, invalid("%v", err)
	}
	tpl.Source = string(source)
	if err := tpl.validate(); err != nil {
		return nil, err
	}
	return &tpl, nil
}

func (t *Template) validate() error {
	switch {
	case t.Format != Format:
		return invalid("format must be %q", Format)
	case !validTemplateID.MatchString(t.ID):
		return invalid("id must be 2-50 lowercase letters, digits or hyphens, starting with a letter")
	case t.Name == "" || len(t.Name) > maxNameLength:
		return invalid("name must be 1-%d characters", maxNameLength)
	case t.Summary == "" || len(t.Summary) > maxSummaryLength:
		return invalid("summary must be 1-%d characters", maxSummaryLength)
	case len(t.Description) > maxDescLength:
		return invalid("description exceeds %d characters", maxDescLength)
	case len(t.Apps) == 0 || len(t.Apps) > MaxTemplateApps:
		return invalid("a template declares 1 to at most %d apps", MaxTemplateApps)
	}
	byName := make(map[string]*AppSpec, len(t.Apps))
	for i := range t.Apps {
		app := &t.Apps[i]
		if byName[app.Name] != nil {
			return invalid("app %q is declared twice", app.Name)
		}
		if err := app.validateShape(); err != nil {
			return err
		}
		byName[app.Name] = app
	}
	for i := range t.Apps {
		if err := t.Apps[i].validateEnv(byName); err != nil {
			return err
		}
	}
	return nil
}

// validateShape holds everything but the variables to the app model's own
// rules, on a probe record. The plan's replica cap is checked on deploy, so
// the probe takes the largest plan.
func (a *AppSpec) validateShape() error {
	if a.Replicas == nil {
		return invalid("app %q: replicas is required (0-%d)", a.Name, apphost.MaxReplicas)
	}
	probe := a.record("probe", "probe", domain.Enterprise)
	if err := probe.Validate(); err != nil {
		return invalid("app %q: %v", a.Name, err)
	}
	return nil
}

func (a *AppSpec) validateEnv(apps map[string]*AppSpec) error {
	seen := make(map[string]bool, len(a.Env))
	for _, v := range a.Env {
		if err := apphost.ValidateEnvName(v.Name); err != nil {
			return invalid("app %q: %v", a.Name, err)
		}
		if seen[v.Name] {
			return invalid("app %q declares %s twice", a.Name, v.Name)
		}
		seen[v.Name] = true
		parts, err := parseValue(v.Value)
		if err != nil {
			return invalid("app %q, %s: %v", a.Name, v.Name, err)
		}
		for _, p := range parts {
			if err := checkTarget(p.expr, apps); err != nil {
				return invalid("app %q, %s: %v", a.Name, v.Name, err)
			}
		}
	}
	if len(a.Env) > apphost.MaxEnvVars {
		return invalid("app %q declares more than %d variables", a.Name, apphost.MaxEnvVars)
	}
	return nil
}

// checkTarget: an app expression names an app of this template, and a
// variable it reads is a plain value — never another app's variable or the
// database, so resolution is one step and has no cycles.
func checkTarget(e *expr, apps map[string]*AppSpec) error {
	if e == nil || e.app == "" {
		return nil
	}
	target := apps[e.app]
	if target == nil {
		return fmt.Errorf("no app %q in this template", e.app)
	}
	if e.kind != exprAppEnv {
		return nil
	}
	for _, v := range target.Env {
		if v.Name != e.variable {
			continue
		}
		parts, err := parseValue(v.Value)
		if err != nil {
			return err
		}
		for _, p := range parts {
			if p.expr != nil && p.expr.kind == exprAppEnv {
				return fmt.Errorf("apps.%s.env.%s points at another app variable; name the value directly", e.app, e.variable)
			}
			if p.expr != nil && p.expr.kind == exprDatabase {
				return fmt.Errorf("apps.%s.env.%s is a database reference; use ${{ db.%s }} directly", e.app, e.variable, p.expr.variable)
			}
		}
		return nil
	}
	return fmt.Errorf("app %q has no variable %s", e.app, e.variable)
}

// record is the app this spec creates, before its variables are resolved.
func (a *AppSpec) record(id, projectID string, tier domain.TierType) *apphost.App {
	app := &apphost.App{
		ID: id, ProjectID: projectID, Name: a.Name, Image: a.Image,
		Port: a.Port, Internal: a.Internal, HealthCheckPath: a.HealthCheckPath,
		Args: cloneStrings(a.Args), Env: []apphost.EnvVar{}, Tier: tier,
	}
	if a.Replicas != nil {
		app.Replicas = *a.Replicas
	}
	app.Status = apphost.StatusFor(app.Replicas)
	for _, port := range a.InternalPorts {
		app.InternalPorts = append(app.InternalPorts, apphost.InternalPort{Port: port, Protocol: apphost.ProtocolTCP})
	}
	if a.Disk != nil {
		app.Disk = &apphost.AppDisk{MountPath: a.Disk.MountPath, Size: a.Disk.Size}
	}
	return app
}

// port is how another app reaches this one inside the project.
func (a *AppSpec) port() int {
	if a.Internal {
		return a.InternalPorts[0]
	}
	return apphost.ServiceHTTPPort
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string{}, values...)
}

// Facts are what a template needs from a project, known before any deploy.
type Facts struct {
	Apps int `json:"apps"`
	// NeedsPrivateNetwork: an app is an internal service, or one app reaches another by host or port.
	NeedsPrivateNetwork bool `json:"needsPrivateNetwork"`
	// NeedsDatabase: a variable references the project's database.
	NeedsDatabase bool `json:"needsDatabase"`
	// GeneratesSecrets: a variable holds secret(n), so the deploy needs a vault.
	GeneratesSecrets bool  `json:"generatesSecrets"`
	DiskBytes        int64 `json:"diskBytes"`
}

func (t *Template) Facts() Facts {
	facts := Facts{Apps: len(t.Apps)}
	for _, app := range t.Apps {
		if app.Internal {
			facts.NeedsPrivateNetwork = true
		}
		if app.Disk != nil {
			bytes, _ := apphost.AppDisk{Size: app.Disk.Size}.Bytes()
			facts.DiskBytes += bytes
		}
		for _, v := range app.Env {
			parts, _ := parseValue(v.Value)
			for _, p := range parts {
				switch {
				case p.expr == nil:
				case p.expr.kind == exprDatabase:
					facts.NeedsDatabase = true
				case p.expr.kind == exprSecret:
					facts.GeneratesSecrets = true
				case (p.expr.kind == exprAppHost || p.expr.kind == exprAppPort) && p.expr.app != app.Name:
					facts.NeedsPrivateNetwork = true
				}
			}
		}
	}
	return facts
}

// ValidID reports whether id could name a template, so a path segment is checked before any lookup.
func ValidID(id string) bool { return validTemplateID.MatchString(id) }
