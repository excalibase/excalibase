package apphost

import "time"

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

// EnvNames only, never values.
type DeploySpec struct {
	Image     string          `json:"image"`
	EnvNames  []string        `json:"envNames"`
	Port      int             `json:"port"`
	Replicas  int             `json:"replicas"`
	Resources DeployResources `json:"resources"`
}

type Deploy struct {
	ID            string     `json:"id"`
	AppID         string     `json:"appId"`
	ProjectID     string     `json:"projectId"`
	Revision      int        `json:"revision"`
	Image         string     `json:"image"`
	Spec          DeploySpec `json:"spec"`
	Status        string     `json:"status"`
	FailureReason string     `json:"failureReason,omitempty"`
	CreatedBy     string     `json:"createdBy"`
	CreatedAt     time.Time  `json:"createdAt"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
}

func EnvNames(env []EnvVar) []string {
	names := make([]string, len(env))
	for i, v := range env {
		names[i] = v.Name
	}
	return names
}
