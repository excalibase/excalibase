package adminconsole

import "time"

// UserRow is one account in the users table.
type UserRow struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Username string `json:"username"`
	// TempMail is nil when no disposable-address list is wired, so the table
	// shows "unknown" rather than a false "no".
	TempMail        *bool      `json:"tempMail,omitempty"`
	EmailVerified   bool       `json:"emailVerified"`
	SignedUpAt      *time.Time `json:"signedUpAt,omitempty"`
	LastLoginAt     *time.Time `json:"lastLoginAt,omitempty"`
	LastLoginIP     string     `json:"lastLoginIp,omitempty"`
	Role            string     `json:"role"`
	Status          string     `json:"status"`
	SuspendedReason string     `json:"suspendedReason,omitempty"`
	SuspendedAt     *time.Time `json:"suspendedAt,omitempty"`
	OrgCount        int        `json:"orgCount"`
	ProjectCount    int        `json:"projectCount"`
	RunningApps     int        `json:"runningApps"`
}

// OrgRow is one org in the orgs table.
type OrgRow struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Slug         string     `json:"slug"`
	Tier         string     `json:"tier"`
	OwnerID      string     `json:"ownerId"`
	OwnerEmail   string     `json:"ownerEmail"`
	OwnerStatus  string     `json:"ownerStatus"`
	ProjectCount int        `json:"projectCount"`
	RunningApps  int        `json:"runningApps"`
	CPUCores     float64    `json:"cpuCores"`
	MemoryMB     float64    `json:"memoryMb"`
	CreatedAt    *time.Time `json:"createdAt,omitempty"`
	// CreditMicros is the org's credit balance; absent while credits are off or the org has no account.
	CreditMicros *int64 `json:"creditMicros,omitempty"`
}

// UserQuery filters the users table. Search matches an email, a domain
// ("@example.com" or "example.com") or a last-login IP.
type UserQuery struct {
	Search           string
	IncludeSuspended bool
	// Sort is one of UserSorts ("" keeps newest first); Desc applies to a named sort.
	Sort string
	Desc bool
	// Status (active, suspended, disabled) and Role narrow the list; "" means any.
	Status string
	Role   string
	Limit  int
	Offset int
}

// OrgQuery filters the orgs table.
type OrgQuery struct {
	Search           string
	IncludeSuspended bool
	// Sort is one of OrgSorts ("" keeps newest first); Desc applies to a named sort.
	Sort string
	Desc bool
	// Tier (FREE, STANDARD, ENTERPRISE) and OwnerStatus narrow the list; "" means any.
	Tier        string
	OwnerStatus string
	Limit       int
	Offset      int
}

// Page is one page of rows and the total the filter matches.
type Page[T any] struct {
	Items []T `json:"items"`
	Total int `json:"total"`
}

// DomainCount is a sign-up email domain and how many accounts used it.
type DomainCount struct {
	Domain string `json:"domain"`
	Count  int    `json:"count"`
}

// ProjectBrief is a project a card on the overview points at.
type ProjectBrief struct {
	ProjectID     string  `json:"projectId"`
	ProjectName   string  `json:"projectName"`
	OrgID         string  `json:"orgId"`
	OwnerEmail    string  `json:"ownerEmail"`
	OwnerStatus   string  `json:"ownerStatus"`
	Status        string  `json:"status"`
	FailureReason string  `json:"failureReason,omitempty"`
	CPUCores      float64 `json:"cpuCores,omitempty"`
	MemoryMB      float64 `json:"memoryMb,omitempty"`
	EgressBytesPS float64 `json:"egressBytesPerSec,omitempty"`
}

// SuspendedBrief is a recently suspended account.
type SuspendedBrief struct {
	UserID      string    `json:"userId"`
	Email       string    `json:"email"`
	Reason      string    `json:"reason"`
	SuspendedAt time.Time `json:"suspendedAt"`
}

// Overview is "needs attention", first on the page.
type Overview struct {
	NewlySuspended []SuspendedBrief `json:"newlySuspended"`
	Signups24h     int              `json:"signups24h"`
	SignupDomains  []DomainCount    `json:"signupDomains"`
	FailedProjects []ProjectBrief   `json:"failedProjects"`
	TopCPU         []ProjectBrief   `json:"topCpu"`
	TopMemory      []ProjectBrief   `json:"topMemory"`
	// TopEgress is read from the single-host container metrics, or from the net
	// meter's samples while FEATURE_NETMETER is on (a cluster records egress
	// per project only then, so it is empty there otherwise).
	TopEgress []ProjectBrief `json:"topEgress"`
}

// ProjectCore is the stored part of a project's detail.
type ProjectCore struct {
	ProjectID     string     `json:"projectId"`
	ProjectName   string     `json:"projectName"`
	OrgID         string     `json:"orgId"`
	OrgName       string     `json:"orgName"`
	OrgSlug       string     `json:"orgSlug"`
	Tier          string     `json:"tier"`
	Namespace     string     `json:"-"`
	OwnerID       string     `json:"ownerId"`
	OwnerEmail    string     `json:"ownerEmail"`
	OwnerStatus   string     `json:"ownerStatus"`
	OwnerReason   string     `json:"ownerSuspendedReason,omitempty"`
	DBType        string     `json:"databaseType"`
	DBVersion     string     `json:"databaseVersion"`
	DBStatus      string     `json:"databaseStatus"`
	NoDatabase    bool       `json:"noDatabase"`
	FailureReason string     `json:"failureReason,omitempty"`
	CPUCores      float64    `json:"cpuCores"`
	MemoryMB      float64    `json:"memoryMb"`
	CreatedAt     *time.Time `json:"createdAt,omitempty"`
}

// SiteBrief and FunctionBrief are what a project serves besides apps.
type SiteBrief struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
}

type FunctionBrief struct {
	ID     string `json:"id"`
	Active bool   `json:"active"`
}

// AuditBrief is a recent audit event on the project.
type AuditBrief struct {
	Action    string     `json:"action"`
	UserID    string     `json:"userId,omitempty"`
	Details   string     `json:"details,omitempty"`
	Timestamp *time.Time `json:"timestamp,omitempty"`
}

// AppView is one app as the admin needs it: what it runs and whether it is.
type AppView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Image    string `json:"image"`
	Replicas int    `json:"replicas"`
	// Status is the recorded lifecycle status; Running is what the runtime
	// answered just now, nil when it could not be asked.
	Status  string `json:"status"`
	Running *int   `json:"runningReplicas"`
	// CPU and memory are the plan's size per replica, the most an app may use.
	CPUMilli  int    `json:"cpuMilliPerReplica"`
	MemoryMiB int    `json:"memoryMibPerReplica"`
	Tier      string `json:"tier"`
	Internal  bool   `json:"internal"`
}

// ProjectDetail answers "who owns this, what does it run, is it OK".
type ProjectDetail struct {
	ProjectCore
	Apps      []AppView       `json:"apps"`
	Sites     []SiteBrief     `json:"sites"`
	Functions []FunctionBrief `json:"functions"`
	Audit     []AuditBrief    `json:"audit"`
}

// ProjectFact is what the project list shows beside the project row: whose it
// is and how much it uses now. Usage is absent without a live sample.
type ProjectFact struct {
	OrgName     string
	OwnerID     string
	OwnerEmail  string
	OwnerStatus string
	HasUsage    bool
	CPUCores    float64
	MemoryMB    float64
}
