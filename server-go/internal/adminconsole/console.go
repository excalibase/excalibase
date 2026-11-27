package adminconsole

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	DefaultPageSize = 50
	MaxPageSize     = 200
	recentAudit     = 15
)

var ErrProjectNotFound = errors.New("project not found")

// ErrInvalidQuery is a list request that asks for a sort or filter the console does not offer.
var ErrInvalidQuery = errors.New("that sort or filter is not available")

// The columns a list may be sorted by and the values its filters take. A
// request's words are only ever checked against these, never put in a query.
var (
	UserSorts   = []string{"created", "email", "lastLogin", "orgs", "projects", "apps"}
	OrgSorts    = []string{"created", "name", "owner", "plan", "projects", "apps"}
	userRoles   = []string{"user", "platform_admin", "platform_operator", "platform_viewer"}
	accountStat = []string{StatusActive, StatusSuspended, StatusDisabled}
	orgTiers    = []string{"FREE", "STANDARD", "ENTERPRISE"}
)

// oneOf reports whether value is empty (no filter) or one of the allowed ones.
func oneOf(value string, allowed []string) bool {
	return value == "" || slices.Contains(allowed, value)
}

// Reader is every query the console's tables and cards are built from.
type Reader interface {
	ListUsers(ctx context.Context, query UserQuery) (Page[UserRow], error)
	ListOrgs(ctx context.Context, query OrgQuery) (Page[OrgRow], error)
	Overview(ctx context.Context, since time.Time) (*Overview, error)
	ProjectCore(ctx context.Context, projectID string) (*ProjectCore, error)
	ProjectSites(ctx context.Context, projectID string) ([]SiteBrief, error)
	ProjectFunctions(ctx context.Context, projectID string) ([]FunctionBrief, error)
	ProjectAudit(ctx context.Context, projectID string, limit int) ([]AuditBrief, error)
}

// AppLister reads a project's apps from the app store.
type AppLister interface {
	List(projectID string) ([]*apphost.App, error)
}

// AppProbe asks the runtime (Kubernetes or a single host) how many replicas serve now.
type AppProbe interface {
	AppAvailableReplicas(ctx context.Context, namespace, name string) (int32, error)
}

// Console answers the admin's questions.
type Console struct {
	reader     Reader
	apps       AppLister
	probe      AppProbe
	tempMail   func(email string) bool
	objectName func(appName string) string
	audit      AuditWriter
	balances   CreditBalances
	budgets    PlanBudgets
	now        func() time.Time
}

func NewConsole(reader Reader, apps AppLister, probe AppProbe) *Console {
	return &Console{reader: reader, apps: apps, probe: probe, objectName: k8s.AppObjectName, now: time.Now}
}

// CreditBalances reads the credit balance of the given orgs; an org with no account is left out.
// It answers nothing while credits are off.
type CreditBalances func(ctx context.Context, orgIDs []string) (map[string]int64, error)

// SetCreditBalances wires the balance column of the orgs table.
func (c *Console) SetCreditBalances(balances CreditBalances) { c.balances = balances }

// OrgBudget is how much of its plan an org holds, for the orgs table.
type OrgBudget struct {
	CPUMilliUsed     int64 `json:"cpuMilliUsed"`
	CPUMilliLimit    int64 `json:"cpuMilliLimit"`
	MemoryBytesUsed  int64 `json:"memoryBytesUsed"`
	MemoryBytesLimit int64 `json:"memoryBytesLimit"`
}

// PlanBudgets reads the plan budget of the given orgs. It answers nothing while the budget is off.
type PlanBudgets func(ctx context.Context, orgIDs []string) (map[string]OrgBudget, error)

// SetPlanBudgets wires the budget column of the orgs table.
func (c *Console) SetPlanBudgets(budgets PlanBudgets) { c.budgets = budgets }

// SetTempMailCheck wires the disposable-address list; without it the flag stays unknown.
func (c *Console) SetTempMailCheck(check func(email string) bool) { c.tempMail = check }

// SetAudit wires the log an opening of a project's data is written to.
func (c *Console) SetAudit(audit AuditWriter) { c.audit = audit }

// SetObjectName overrides how an app's name becomes its workload's name.
func (c *Console) SetObjectName(name func(appName string) string) { c.objectName = name }

func clampPage(limit, offset int) (int, int) {
	if limit <= 0 {
		limit = DefaultPageSize
	}
	if limit > MaxPageSize {
		limit = MaxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

// Users lists accounts, suspended ones only when asked for.
func (c *Console) Users(ctx context.Context, query UserQuery) (Page[UserRow], error) {
	if !oneOf(query.Sort, UserSorts) || !oneOf(query.Status, accountStat) || !oneOf(query.Role, userRoles) {
		return Page[UserRow]{}, ErrInvalidQuery
	}
	query.IncludeSuspended = query.IncludeSuspended || query.Status == StatusSuspended
	query.Limit, query.Offset = clampPage(query.Limit, query.Offset)
	page, err := c.reader.ListUsers(ctx, query)
	if err != nil {
		return Page[UserRow]{}, fmt.Errorf("list users: %w", err)
	}
	if c.tempMail != nil {
		for i := range page.Items {
			flagged := c.tempMail(page.Items[i].Email)
			page.Items[i].TempMail = &flagged
		}
	}
	return page, nil
}

// Orgs lists orgs with their owner's status and what they run.
func (c *Console) Orgs(ctx context.Context, query OrgQuery) (Page[OrgRow], error) {
	query.Tier = strings.ToUpper(query.Tier)
	if !oneOf(query.Sort, OrgSorts) || !oneOf(query.OwnerStatus, accountStat) || !oneOf(query.Tier, orgTiers) {
		return Page[OrgRow]{}, ErrInvalidQuery
	}
	query.IncludeSuspended = query.IncludeSuspended || query.OwnerStatus == StatusSuspended
	query.Limit, query.Offset = clampPage(query.Limit, query.Offset)
	page, err := c.reader.ListOrgs(ctx, query)
	if err != nil {
		return Page[OrgRow]{}, fmt.Errorf("list orgs: %w", err)
	}
	c.addCreditBalances(ctx, page.Items)
	c.addPlanBudgets(ctx, page.Items)
	return page, nil
}

// Overview is the "needs attention" block.
func (c *Console) Overview(ctx context.Context) (*Overview, error) {
	overview, err := c.reader.Overview(ctx, c.now().Add(-24*time.Hour))
	if err != nil {
		return nil, fmt.Errorf("overview: %w", err)
	}
	return overview, nil
}

// Project is one project's detail: owner, database, apps with their running
// state, sites, functions and recent audit events.
func (c *Console) Project(ctx context.Context, actor Actor, projectID, reason string) (*ProjectDetail, error) {
	reason, err := cleanReason(reason)
	if err != nil {
		return nil, err
	}
	core, err := c.reader.ProjectCore(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("read project: %w", err)
	}
	if core == nil {
		return nil, ErrProjectNotFound
	}
	c.recordOpen(ctx, actor, projectID, reason)
	detail := &ProjectDetail{ProjectCore: *core, Apps: []AppView{}, Sites: []SiteBrief{}, Functions: []FunctionBrief{}, Audit: []AuditBrief{}}
	if detail.Apps, err = c.appViews(ctx, core); err != nil {
		return nil, err
	}
	if detail.Sites, err = c.reader.ProjectSites(ctx, projectID); err != nil {
		return nil, fmt.Errorf("read sites: %w", err)
	}
	if detail.Functions, err = c.reader.ProjectFunctions(ctx, projectID); err != nil {
		return nil, fmt.Errorf("read functions: %w", err)
	}
	if detail.Audit, err = c.reader.ProjectAudit(ctx, projectID, recentAudit); err != nil {
		return nil, fmt.Errorf("read audit: %w", err)
	}
	return detail, nil
}

func (c *Console) appViews(ctx context.Context, core *ProjectCore) ([]AppView, error) {
	views := []AppView{}
	if c.apps == nil {
		return views, nil
	}
	apps, err := c.apps.List(core.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	for _, app := range apps {
		view := AppView{ID: app.ID, Name: app.Name, Image: app.Image, Replicas: app.Replicas, Status: app.Status, Tier: string(app.Tier), Internal: app.Internal}
		view.CPUMilli, view.MemoryMiB = podSize(app)
		view.Running = c.runningReplicas(ctx, core.Namespace, app)
		views = append(views, view)
	}
	return views, nil
}

// runningReplicas asks the runtime; nil says it could not be asked, which is
// not the same as zero pods.
func (c *Console) runningReplicas(ctx context.Context, namespace string, app *apphost.App) *int {
	if c.probe == nil || namespace == "" {
		return nil
	}
	available, err := c.probe.AppAvailableReplicas(ctx, namespace, c.objectName(app.Name))
	if err != nil {
		log.Printf("admin console: read serving replicas of %s/%s: %v", namespace, app.Name, err)
		return nil
	}
	running := int(available)
	return &running
}

// podSize is the plan's size for one replica; zero when the tier is unknown.
func podSize(app *apphost.App) (cpuMilli, memoryMiB int) {
	size, err := config.GetAppTierConfig(app.Tier)
	if err != nil {
		return 0, 0
	}
	if cpu, err := resource.ParseQuantity(size.CPULimit); err == nil {
		cpuMilli = int(cpu.MilliValue())
	}
	if memory, err := resource.ParseQuantity(size.MemoryLimit); err == nil {
		memoryMiB = int(memory.Value() / (1024 * 1024))
	}
	return cpuMilli, memoryMiB
}

// recordOpen writes who opened a customer's project and why, before any of it
// is read. A failed write is logged, not fatal: the log is for the owner's
// review, and a down audit table must not hide a failing project.
func (c *Console) recordOpen(ctx context.Context, actor Actor, projectID, reason string) {
	if c.audit == nil {
		return
	}
	entry := &domain.AuditEntry{UserID: actor.ID, Action: AuditActionProjectOpen, Resource: "project", ResourceID: projectID,
		ProjectID: projectID, Details: reason, IPAddress: actor.IP}
	if err := c.audit.LogAudit(ctx, entry); err != nil {
		log.Printf("ERROR: audit %s %s: %v", AuditActionProjectOpen, projectID, err)
	}
}

// addCreditBalances fills the balance column. A failed lookup leaves the column empty:
// the credit is context for the table, not a reason to hide the orgs.
func (c *Console) addCreditBalances(ctx context.Context, rows []OrgRow) {
	if c.balances == nil || len(rows) == 0 {
		return
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	balances, err := c.balances(ctx, ids)
	if err != nil {
		log.Printf("WARN: admin console: credit balances: %v", err)
		return
	}
	for i := range rows {
		if micros, ok := balances[rows[i].ID]; ok {
			rows[i].CreditMicros = &micros
		}
	}
}

// addPlanBudgets fills the budget column. A failed lookup leaves it empty, like the credit column.
func (c *Console) addPlanBudgets(ctx context.Context, rows []OrgRow) {
	if c.budgets == nil || len(rows) == 0 {
		return
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	budgets, err := c.budgets(ctx, ids)
	if err != nil {
		log.Printf("WARN: admin console: plan budgets: %v", err)
		return
	}
	for i := range rows {
		if budget, ok := budgets[rows[i].ID]; ok {
			rows[i].Budget = &budget
		}
	}
}
