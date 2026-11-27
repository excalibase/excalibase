package planbudget

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// OrgLockSpace is the advisory-lock space the per-org lock lives in. Creating a
// project, deleting an org and every budget step queue on this one lock.
const OrgLockSpace = 421

// Querier is the part of *sql.Tx the gate uses; every step runs inside the
// caller's own transaction.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Gate admits growth against an org's plan. The zero value admits everything.
type Gate struct {
	on    func(context.Context) bool
	plans config.PlanBudgetConfig
}

// NewGate reads the plan numbers once; on says whether the feature is on now.
func NewGate(on func(context.Context) bool, plans config.PlanBudgetConfig) *Gate {
	return &Gate{on: on, plans: plans}
}

// Active reports whether the gate checks anything now.
func (g *Gate) Active(ctx context.Context) bool {
	return g != nil && g.on != nil && g.on(ctx)
}

// Ceiling is what a plan allows; ok is false for a plan with no budget.
func (g *Gate) Ceiling(tier string) (Size, bool) {
	var plan config.PlanNumbers
	switch strings.ToUpper(tier) {
	case string(domain.Free):
		plan = g.plans.Free
	case string(domain.Standard):
		plan = g.plans.Standard
	default:
		return Size{}, false
	}
	return Size{
		CPUMilli:    int64(plan.CPUMilli),
		MemoryBytes: int64(plan.MemoryMiB) << 20,
		DiskBytes:   int64(plan.DiskGiB) << 30,
		Services:    int64(plan.MaxServices),
	}, true
}

// Delta says how much a step grows or shrinks the org's reservation. It is
// called after the org lock is held, so it reads state nobody else is changing.
type Delta func(ctx context.Context) (Size, error)

// Admit takes the org lock, then, for a step that grows something, refuses it
// when the org's reservation plus the growth passes its ceiling. The caller
// writes its own state change in the same transaction before it commits.
// A step that only shrinks never reads the usage and always passes.
func (g *Gate) Admit(ctx context.Context, tx Querier, orgID string, delta Delta) error {
	if !g.Active(ctx) || orgID == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1, hashtext($2))`, OrgLockSpace, orgID); err != nil {
		return fmt.Errorf("lock org budget: %w", err)
	}
	change, err := delta(ctx)
	if err != nil {
		return err
	}
	if !growsAnything(change) {
		return nil
	}
	tier, err := orgTier(ctx, tx, orgID)
	if err != nil {
		return err
	}
	ceiling, limited := g.Ceiling(tier)
	if !limited {
		return nil
	}
	used, err := OrgUsage(ctx, tx, orgID)
	if err != nil {
		return err
	}
	return Decide(used, change, ceiling)
}

func growsAnything(s Size) bool {
	return s.CPUMilli > 0 || s.MemoryBytes > 0 || s.DiskBytes > 0 || s.Services > 0
}

func orgTier(ctx context.Context, tx Querier, orgID string) (string, error) {
	var tier string
	err := tx.QueryRowContext(ctx, `SELECT tier FROM orgs WHERE id = $1`, orgID).Scan(&tier)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read org plan: %w", err)
	}
	return tier, nil
}

// OrgUsage sums what the org's databases, apps and functions runtimes reserve.
func OrgUsage(ctx context.Context, q Querier, orgID string) (Size, error) {
	var total Size
	databases, err := q.QueryContext(ctx, `
		SELECT tier, status, COALESCE(storage_size, ''), reserved_cpu_milli, reserved_memory_bytes, reserved_disk_bytes, reserved_copies, has_functions_runtime
		FROM database_instances WHERE org_id = $1`, orgID)
	if err != nil {
		return total, fmt.Errorf("read org databases: %w", err)
	}
	defer databases.Close()
	for databases.Next() {
		var db Database
		var tier, storageSize string
		var hasRuntime bool
		if err := databases.Scan(&tier, &db.Status, &storageSize, &db.PerCopy.CPUMilli, &db.PerCopy.MemoryBytes, &db.PerCopy.DiskBytes, &db.Copies, &hasRuntime); err != nil {
			return total, fmt.Errorf("scan org database: %w", err)
		}
		if disk, err := ParseBytes(storageSize); err == nil && storageSize != "" {
			db.PerCopy.DiskBytes = disk
		}
		total = total.Add(ReservedDatabase(db))
		if hasRuntime {
			total = total.Add(ReservedFunctions(tier))
		}
	}
	if err := databases.Err(); err != nil {
		return total, err
	}
	apps, err := q.QueryContext(ctx, `
		SELECT a.doc,
		       EXISTS (SELECT 1 FROM app_deploys r WHERE r.project_id = a.project_id AND r.app_id = a.id AND r.status IN ('pending', 'rolling'))
		FROM apps a JOIN database_instances d ON d.project_id = a.project_id WHERE d.org_id = $1`, orgID)
	if err != nil {
		return total, fmt.Errorf("read org apps: %w", err)
	}
	defer apps.Close()
	for apps.Next() {
		var doc []byte
		var deploying bool
		if err := apps.Scan(&doc, &deploying); err != nil {
			return total, fmt.Errorf("scan org app: %w", err)
		}
		size, err := ReservedAppNow(doc, deploying)
		if err != nil {
			return total, err
		}
		total = total.Add(size)
	}
	return total, apps.Err()
}

// ReservedAppNow is what an app holds now. An app with a deploy in flight holds
// what it will run at, so the deploy's own row is what the next admission sees.
func ReservedAppNow(doc []byte, deploying bool) (Size, error) {
	if deploying {
		return ReservedAppAs(doc, runningStatus)
	}
	return ReservedApp(doc)
}

// runningStatus is the app status of a converged rollout (apphost.StatusRunning).
const runningStatus = string(domain.StatusActive)
