package apphost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/planbudget"
)

// defaultBudget is the gate every store built after SetDefaultPlanBudget starts
// with. The platform has one plan budget, set once at boot, and apps are written
// by many services; one default means none of them can be built without it.
var defaultBudget *planbudget.Gate

// SetDefaultPlanBudget sets the gate new app and deploy stores admit through.
// Call it once at boot, before any store is built.
func SetDefaultPlanBudget(gate *planbudget.Gate) { defaultBudget = gate }

// projectOrg reads the organisation a project belongs to; a project with no row
// has no budget to hold.
func projectOrg(ctx context.Context, tx *sql.Tx, projectID string) (string, error) {
	var orgID string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(org_id, '') FROM database_instances WHERE project_id = $1`, projectID).Scan(&orgID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read project org: %w", err)
	}
	return orgID, nil
}

// admitApp admits the growth of one app inside the caller's transaction. delta
// runs after the org lock is held, and the caller writes its own change before
// it commits.
func admitApp(ctx context.Context, gate *planbudget.Gate, tx *sql.Tx, projectID string, delta planbudget.Delta) error {
	if !gate.Active(ctx) {
		return nil
	}
	orgID, err := projectOrg(ctx, tx, projectID)
	if err != nil {
		return err
	}
	return gate.Admit(ctx, tx, orgID, delta)
}

// growth is what an app record holds after a change, less what it held before.
func growth(before, after []byte, status string) (planbudget.Size, error) {
	held, err := planbudget.ReservedApp(before)
	if err != nil {
		return planbudget.Size{}, err
	}
	next, err := planbudget.ReservedAppAs(after, status)
	if err != nil {
		return planbudget.Size{}, err
	}
	return next.Sub(held), nil
}
