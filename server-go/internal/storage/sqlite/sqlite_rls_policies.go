package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// ErrPolicyNotFound mirrors the postgres impl's sentinel so the handler
// layer can do `errors.Is(err, ErrPolicyNotFound)` against whichever
// backend is wired.
var ErrPolicyNotFound = errors.New("policy not found")

// RlsPolicyStore is the SQLite mirror of postgres.RlsPolicyStore.
// All array columns are JSON-encoded text since SQLite has no array type.
type RlsPolicyStore struct{ s *Store }

func NewRlsPolicies(s *Store) *RlsPolicyStore { return &RlsPolicyStore{s: s} }

// RlsPolicies on *Store satisfies storage.PlatformStore.RlsPolicies.
func (s *Store) RlsPolicies() storage.RlsPolicyStore { return NewRlsPolicies(s) }

// ------------------------- RLS (row-level) -------------------------

func (r *RlsPolicyStore) ListRls(ctx context.Context, projectID, resource string) ([]domain.Policy, error) {
	q := `SELECT id, project_id, name, resource, effect, operations, rule_logic,
	             rules, assignments, priority, enabled, created_at, updated_at
	      FROM rls_policies WHERE project_id = ?`
	args := []any{projectID}
	if resource != "" {
		q += ` AND resource = ?`
		args = append(args, resource)
	}
	q += ` ORDER BY priority, created_at`

	rows, err := r.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query rls_policies: %w", err)
	}
	defer rows.Close()

	out := []domain.Policy{}
	for rows.Next() {
		p, err := scanRls(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *RlsPolicyStore) GetRls(ctx context.Context, projectID, id string) (*domain.Policy, error) {
	row := r.s.db.QueryRowContext(ctx, `
		SELECT id, project_id, name, resource, effect, operations, rule_logic,
		       rules, assignments, priority, enabled, created_at, updated_at
		FROM rls_policies WHERE project_id = ? AND id = ?`, projectID, id)
	p, err := scanRls(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPolicyNotFound
	}
	return p, err
}

func (r *RlsPolicyStore) UpsertRls(ctx context.Context, p *domain.Policy) error {
	ops, _ := json.Marshal(operationsToStrings(p.Operations))
	rules, err := json.Marshal(p.Rules)
	if err != nil {
		return fmt.Errorf("marshal rules: %w", err)
	}
	assigns, err := json.Marshal(p.Assignments)
	if err != nil {
		return fmt.Errorf("marshal assignments: %w", err)
	}
	enabled := 0
	if p.Enabled {
		enabled = 1
	}
	_, err = r.s.db.ExecContext(ctx, `
		INSERT INTO rls_policies (id, project_id, name, resource, effect, operations,
		                          rule_logic, rules, assignments, priority, enabled,
		                          created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			name        = excluded.name,
			resource    = excluded.resource,
			effect      = excluded.effect,
			operations  = excluded.operations,
			rule_logic  = excluded.rule_logic,
			rules       = excluded.rules,
			assignments = excluded.assignments,
			priority    = excluded.priority,
			enabled     = excluded.enabled,
			updated_at  = CURRENT_TIMESTAMP`,
		p.ID, p.ProjectID, p.Name, p.Resource, string(p.Effect), string(ops),
		string(p.RuleLogic), string(rules), string(assigns), p.Priority, enabled)
	return err
}

func (r *RlsPolicyStore) DeleteRls(ctx context.Context, projectID, id string) error {
	res, err := r.s.db.ExecContext(ctx,
		`DELETE FROM rls_policies WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrPolicyNotFound
	}
	return nil
}

// ------------------------- CLS (column-level) -------------------------

func (r *RlsPolicyStore) ListColumn(ctx context.Context, projectID, resource string) ([]domain.ColumnPolicy, error) {
	q := `SELECT id, project_id, name, resource, columns, operations, mode,
	             partial_spec, custom_masker_key, assignments, priority, enabled,
	             created_at, updated_at
	      FROM column_policies WHERE project_id = ?`
	args := []any{projectID}
	if resource != "" {
		q += ` AND resource = ?`
		args = append(args, resource)
	}
	q += ` ORDER BY priority, created_at`

	rows, err := r.s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query column_policies: %w", err)
	}
	defer rows.Close()

	out := []domain.ColumnPolicy{}
	for rows.Next() {
		p, err := scanColumn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (r *RlsPolicyStore) GetColumn(ctx context.Context, projectID, id string) (*domain.ColumnPolicy, error) {
	row := r.s.db.QueryRowContext(ctx, `
		SELECT id, project_id, name, resource, columns, operations, mode,
		       partial_spec, custom_masker_key, assignments, priority, enabled,
		       created_at, updated_at
		FROM column_policies WHERE project_id = ? AND id = ?`, projectID, id)
	p, err := scanColumn(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPolicyNotFound
	}
	return p, err
}

func (r *RlsPolicyStore) UpsertColumn(ctx context.Context, p *domain.ColumnPolicy) error {
	cols, _ := json.Marshal(p.Columns)
	ops, _ := json.Marshal(operationsToStrings(p.Operations))
	assigns, err := json.Marshal(p.Assignments)
	if err != nil {
		return fmt.Errorf("marshal assignments: %w", err)
	}
	var partialJSON sql.NullString
	if p.PartialSpec != nil {
		b, err := json.Marshal(p.PartialSpec)
		if err != nil {
			return fmt.Errorf("marshal partial spec: %w", err)
		}
		partialJSON = sql.NullString{String: string(b), Valid: true}
	}
	customKey := sql.NullString{String: p.CustomMaskerKey, Valid: p.CustomMaskerKey != ""}
	enabled := 0
	if p.Enabled {
		enabled = 1
	}
	_, err = r.s.db.ExecContext(ctx, `
		INSERT INTO column_policies (id, project_id, name, resource, columns, operations,
		                              mode, partial_spec, custom_masker_key, assignments,
		                              priority, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			name              = excluded.name,
			resource          = excluded.resource,
			columns           = excluded.columns,
			operations        = excluded.operations,
			mode              = excluded.mode,
			partial_spec      = excluded.partial_spec,
			custom_masker_key = excluded.custom_masker_key,
			assignments       = excluded.assignments,
			priority          = excluded.priority,
			enabled           = excluded.enabled,
			updated_at        = CURRENT_TIMESTAMP`,
		p.ID, p.ProjectID, p.Name, p.Resource, string(cols), string(ops),
		string(p.Mode), partialJSON, customKey, string(assigns), p.Priority, enabled)
	return err
}

func (r *RlsPolicyStore) DeleteColumn(ctx context.Context, projectID, id string) error {
	res, err := r.s.db.ExecContext(ctx,
		`DELETE FROM column_policies WHERE project_id = ? AND id = ?`, projectID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrPolicyNotFound
	}
	return nil
}

// ------------------------- scan helpers -------------------------
// rowScanner is defined in sqlite_tokens.go; reuse it here.

func scanRls(r rowScanner) (*domain.Policy, error) {
	var (
		p         domain.Policy
		effect    string
		ruleLogic string
		opsRaw    string
		rulesRaw  string
		assignsRaw string
		enabled   int
	)
	if err := r.Scan(&p.ID, &p.ProjectID, &p.Name, &p.Resource, &effect, &opsRaw,
		&ruleLogic, &rulesRaw, &assignsRaw, &p.Priority, &enabled,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Effect = domain.PolicyEffect(effect)
	p.RuleLogic = domain.LogicOperator(ruleLogic)
	p.Enabled = enabled != 0
	var opStrings []string
	if err := json.Unmarshal([]byte(opsRaw), &opStrings); err != nil {
		return nil, fmt.Errorf("unmarshal operations: %w", err)
	}
	p.Operations = stringSliceToOps(opStrings)
	if err := json.Unmarshal([]byte(rulesRaw), &p.Rules); err != nil {
		return nil, fmt.Errorf("unmarshal rules: %w", err)
	}
	if err := json.Unmarshal([]byte(assignsRaw), &p.Assignments); err != nil {
		return nil, fmt.Errorf("unmarshal assignments: %w", err)
	}
	return &p, nil
}

func scanColumn(r rowScanner) (*domain.ColumnPolicy, error) {
	var (
		p          domain.ColumnPolicy
		mode       string
		colsRaw    string
		opsRaw     string
		partialRaw sql.NullString
		customKey  sql.NullString
		assignsRaw string
		enabled    int
	)
	if err := r.Scan(&p.ID, &p.ProjectID, &p.Name, &p.Resource, &colsRaw, &opsRaw,
		&mode, &partialRaw, &customKey, &assignsRaw, &p.Priority, &enabled,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Mode = domain.MaskMode(mode)
	p.Enabled = enabled != 0
	if err := json.Unmarshal([]byte(colsRaw), &p.Columns); err != nil {
		return nil, fmt.Errorf("unmarshal columns: %w", err)
	}
	var opStrings []string
	if err := json.Unmarshal([]byte(opsRaw), &opStrings); err != nil {
		return nil, fmt.Errorf("unmarshal operations: %w", err)
	}
	p.Operations = stringSliceToOps(opStrings)
	if customKey.Valid {
		p.CustomMaskerKey = customKey.String
	}
	if partialRaw.Valid && partialRaw.String != "" && partialRaw.String != "null" {
		var spec domain.PartialMaskSpec
		if err := json.Unmarshal([]byte(partialRaw.String), &spec); err != nil {
			return nil, fmt.Errorf("unmarshal partial_spec: %w", err)
		}
		p.PartialSpec = &spec
	}
	if err := json.Unmarshal([]byte(assignsRaw), &p.Assignments); err != nil {
		return nil, fmt.Errorf("unmarshal assignments: %w", err)
	}
	return &p, nil
}

func operationsToStrings(ops []domain.Operation) []string {
	out := make([]string, len(ops))
	for i, o := range ops {
		out[i] = string(o)
	}
	return out
}

func stringSliceToOps(ss []string) []domain.Operation {
	out := make([]domain.Operation, len(ss))
	for i, s := range ss {
		out[i] = domain.Operation(s)
	}
	return out
}
