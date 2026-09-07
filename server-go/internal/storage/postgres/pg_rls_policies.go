package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/lib/pq"
)

// ErrPolicyNotFound is returned when a Get by id finds no row.
var ErrPolicyNotFound = errors.New("policy not found")

// RlsPolicyStore wraps Store to implement storage.RlsPolicyStore.
// One repo, two tables (rls_policies, column_policies).
type RlsPolicyStore struct{ s *Store }

func NewRlsPolicies(s *Store) *RlsPolicyStore { return &RlsPolicyStore{s: s} }

// RlsPolicies on *Store satisfies storage.PlatformStore.RlsPolicies.
func (s *Store) RlsPolicies() storage.RlsPolicyStore { return NewRlsPolicies(s) }

// ------------------------- RLS (row-level) -------------------------

func (r *RlsPolicyStore) ListRls(ctx context.Context, projectID, resource string) ([]domain.Policy, error) {
	q := `SELECT id, project_id, name, resource, effect, operations, rule_logic,
	             rules, assignments, priority, enabled, created_at, updated_at
	      FROM rls_policies
	      WHERE project_id = $1`
	args := []any{projectID}
	if resource != "" {
		q += ` AND resource = $2`
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
		FROM rls_policies WHERE project_id = $1 AND id = $2`, projectID, id)
	p, err := scanRls(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPolicyNotFound
	}
	return p, err
}

func (r *RlsPolicyStore) UpsertRls(ctx context.Context, p *domain.Policy) error {
	rules, err := json.Marshal(p.Rules)
	if err != nil {
		return fmt.Errorf("marshal rules: %w", err)
	}
	assigns, err := json.Marshal(p.Assignments)
	if err != nil {
		return fmt.Errorf("marshal assignments: %w", err)
	}
	ops := operationsToStrings(p.Operations)

	// The WHERE on DO UPDATE guards project ownership: a conflicting id owned by
	// a different project is neither inserted nor updated (0 rows), so one
	// project can't overwrite another's policy by reusing its id (SEC-H1).
	res, err := r.s.db.ExecContext(ctx, `
		INSERT INTO rls_policies (id, project_id, name, resource, effect, operations,
		                          rule_logic, rules, assignments, priority, enabled,
		                          created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9::jsonb, $10, $11, NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET
			name        = EXCLUDED.name,
			resource    = EXCLUDED.resource,
			effect      = EXCLUDED.effect,
			operations  = EXCLUDED.operations,
			rule_logic  = EXCLUDED.rule_logic,
			rules       = EXCLUDED.rules,
			assignments = EXCLUDED.assignments,
			priority    = EXCLUDED.priority,
			enabled     = EXCLUDED.enabled,
			updated_at  = NOW()
		WHERE rls_policies.project_id = EXCLUDED.project_id`,
		p.ID, p.ProjectID, p.Name, p.Resource, string(p.Effect), pq.Array(ops),
		string(p.RuleLogic), string(rules), string(assigns), p.Priority, p.Enabled)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("rls policy %q is owned by another project", p.ID)
	}
	return nil
}

func (r *RlsPolicyStore) DeleteRls(ctx context.Context, projectID, id string) error {
	res, err := r.s.db.ExecContext(ctx,
		`DELETE FROM rls_policies WHERE project_id = $1 AND id = $2`, projectID, id)
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
	      FROM column_policies
	      WHERE project_id = $1`
	args := []any{projectID}
	if resource != "" {
		q += ` AND resource = $2`
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
		FROM column_policies WHERE project_id = $1 AND id = $2`, projectID, id)
	p, err := scanColumn(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPolicyNotFound
	}
	return p, err
}

func (r *RlsPolicyStore) UpsertColumn(ctx context.Context, p *domain.ColumnPolicy) error {
	var partialJSON sql.NullString
	if p.PartialSpec != nil {
		b, err := json.Marshal(p.PartialSpec)
		if err != nil {
			return fmt.Errorf("marshal partial spec: %w", err)
		}
		partialJSON = sql.NullString{String: string(b), Valid: true}
	}
	assigns, err := json.Marshal(p.Assignments)
	if err != nil {
		return fmt.Errorf("marshal assignments: %w", err)
	}
	ops := operationsToStrings(p.Operations)
	customKey := sql.NullString{String: p.CustomMaskerKey, Valid: p.CustomMaskerKey != ""}

	// project-ownership guard, same as UpsertRls (SEC-H1).
	res, err := r.s.db.ExecContext(ctx, `
		INSERT INTO column_policies (id, project_id, name, resource, columns, operations,
		                              mode, partial_spec, custom_masker_key, assignments,
		                              priority, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10::jsonb, $11, $12, NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET
			name              = EXCLUDED.name,
			resource          = EXCLUDED.resource,
			columns           = EXCLUDED.columns,
			operations        = EXCLUDED.operations,
			mode              = EXCLUDED.mode,
			partial_spec      = EXCLUDED.partial_spec,
			custom_masker_key = EXCLUDED.custom_masker_key,
			assignments       = EXCLUDED.assignments,
			priority          = EXCLUDED.priority,
			enabled           = EXCLUDED.enabled,
			updated_at        = NOW()
		WHERE column_policies.project_id = EXCLUDED.project_id`,
		p.ID, p.ProjectID, p.Name, p.Resource, pq.Array(p.Columns), pq.Array(ops),
		string(p.Mode), partialJSON, customKey, string(assigns), p.Priority, p.Enabled)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("column policy %q is owned by another project", p.ID)
	}
	return nil
}

func (r *RlsPolicyStore) DeleteColumn(ctx context.Context, projectID, id string) error {
	res, err := r.s.db.ExecContext(ctx,
		`DELETE FROM column_policies WHERE project_id = $1 AND id = $2`, projectID, id)
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

// rowScanner is the common interface of *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanRls(r rowScanner) (*domain.Policy, error) {
	var (
		p          domain.Policy
		effect     string
		ruleLogic  string
		ops        pq.StringArray
		rules      []byte
		assigns    []byte
	)
	if err := r.Scan(&p.ID, &p.ProjectID, &p.Name, &p.Resource, &effect, &ops,
		&ruleLogic, &rules, &assigns, &p.Priority, &p.Enabled,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Effect = domain.PolicyEffect(effect)
	p.RuleLogic = domain.LogicOperator(ruleLogic)
	p.Operations = stringsToOperations(ops)
	if err := json.Unmarshal(rules, &p.Rules); err != nil {
		return nil, fmt.Errorf("unmarshal rules: %w", err)
	}
	if err := json.Unmarshal(assigns, &p.Assignments); err != nil {
		return nil, fmt.Errorf("unmarshal assignments: %w", err)
	}
	return &p, nil
}

func scanColumn(r rowScanner) (*domain.ColumnPolicy, error) {
	var (
		p           domain.ColumnPolicy
		mode        string
		ops         pq.StringArray
		cols        pq.StringArray
		partialRaw  sql.NullString
		customKey   sql.NullString
		assigns     []byte
	)
	if err := r.Scan(&p.ID, &p.ProjectID, &p.Name, &p.Resource, &cols, &ops,
		&mode, &partialRaw, &customKey, &assigns, &p.Priority, &p.Enabled,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Mode = domain.MaskMode(mode)
	p.Columns = []string(cols)
	p.Operations = stringsToOperations(ops)
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
	if err := json.Unmarshal(assigns, &p.Assignments); err != nil {
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

func stringsToOperations(ss pq.StringArray) []domain.Operation {
	out := make([]domain.Operation, len(ss))
	for i, s := range ss {
		out[i] = domain.Operation(s)
	}
	return out
}
