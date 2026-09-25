package apphost

import (
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Superseded means a newer deploy of the same app was created while this
// one was still pending or rolling; the newest deploy always wins.
const (
	DeployStatusPending    = "pending"
	DeployStatusRolling    = "rolling"
	DeployStatusSucceeded  = "succeeded"
	DeployStatusFailed     = "failed"
	DeployStatusSuperseded = "superseded"
)

type DeployResources struct {
	CPURequest    string `json:"cpuRequest"`
	CPULimit      string `json:"cpuLimit"`
	MemoryRequest string `json:"memoryRequest"`
	MemoryLimit   string `json:"memoryLimit"`
}

// EnvSummary is one variable as the API may see it: what it is called and
// what kind it is, never what it holds.
type EnvSummary struct {
	Name string  `json:"name"`
	Kind VarKind `json:"kind"`
}

// DeploySpec is the masked view of what a deploy ran: names and kinds only,
// never a literal value or a secret reference.
type DeploySpec struct {
	Image     string          `json:"image"`
	Env       []EnvSummary    `json:"env"`
	Port      int             `json:"port"`
	Replicas  int             `json:"replicas"`
	Resources DeployResources `json:"resources"`
	// URL is where this deploy is served.
	URL string `json:"url,omitempty"`
}

// DeployConfig is the full app config a deploy froze and rolled out: literal
// values as given, references and secrets as the pointers they are. It is
// never sent to the API — it is what a redeploy rolls out again unchanged.
type DeployConfig struct {
	Image           string          `json:"image"`
	Env             []EnvVar        `json:"env"`
	Port            int             `json:"port"`
	HealthCheckPath string          `json:"healthCheckPath,omitempty"`
	Replicas        int             `json:"replicas"`
	Tier            domain.TierType `json:"tier"`
}

// ConfigFromApp freezes the app's current config into a deploy's config. The
// env slice is copied so a later edit to the app cannot reach back into an
// already-recorded deploy.
func ConfigFromApp(app *App) DeployConfig {
	env := make([]EnvVar, len(app.Env))
	copy(env, app.Env)
	return DeployConfig{
		Image:           app.Image,
		Env:             env,
		Port:            app.Port,
		HealthCheckPath: app.HealthCheckPath,
		Replicas:        app.Replicas,
		Tier:            app.Tier,
	}
}

// ToApp rebuilds a renderable App from a frozen config. Identity (id, project,
// name) comes from the app the deploy belongs to, never from the config,
// since a deploy freezes what runs, not who the app is.
func (c DeployConfig) ToApp(id, projectID, name string) *App {
	return &App{
		ID: id, ProjectID: projectID, Name: name,
		Image:           c.Image,
		Env:             c.Env,
		Port:            c.Port,
		HealthCheckPath: c.HealthCheckPath,
		Replicas:        c.Replicas,
		Tier:            c.Tier,
		Status:          StatusFor(c.Replicas),
	}
}

type Deploy struct {
	ID        string     `json:"id"`
	AppID     string     `json:"appId"`
	ProjectID string     `json:"projectId"`
	Revision  int        `json:"revision"`
	Image     string     `json:"image"`
	Spec      DeploySpec `json:"spec"`
	// Config never reaches the API response: it is marshaled directly by the
	// store, not through this struct's JSON tags.
	Config        DeployConfig `json:"-"`
	RedeployOf    string       `json:"redeployOf,omitempty"`
	Status        string       `json:"status"`
	FailureReason string       `json:"failureReason,omitempty"`
	CreatedBy     string       `json:"createdBy"`
	CreatedAt     time.Time    `json:"createdAt"`
	FinishedAt    *time.Time   `json:"finishedAt,omitempty"`
}

// EnvSummaries lists names and kinds only, never values.
func EnvSummaries(env []EnvVar) []EnvSummary {
	out := make([]EnvSummary, len(env))
	for i, v := range env {
		out[i] = EnvSummary{Name: v.Name, Kind: v.Kind}
	}
	return out
}
