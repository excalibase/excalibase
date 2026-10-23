package advisors

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// DefaultLargeTableRows is the estimated row count from which a table read
// mostly by full scans is reported.
const DefaultLargeTableRows = 100_000

// Pools opens a project's database (projectdb.Opener): its pools carry the
// statement timeout and the read deadline (EXC-650).
type Pools interface {
	Open(ctx context.Context, projectID string) (*sql.DB, error)
	OpenMySQL(ctx context.Context, projectID string, script bool) (*sql.DB, error)
}

// Permissions is the platform's store of API permissions.
type Permissions interface {
	Document(ctx context.Context, projectID string) (*domain.PermissionDocument, error)
}

type Options struct {
	LargeTableRows int64
}

func (o Options) withDefaults() Options {
	if o.LargeTableRows <= 0 {
		o.LargeTableRows = DefaultLargeTableRows
	}
	return o
}

type Service struct {
	pools       Pools
	permissions Permissions
	opts        Options
}

func NewService(pools Pools, permissions Permissions, opts Options) *Service {
	return &Service{pools: pools, permissions: permissions, opts: opts.withDefaults()}
}

// Run checks one project. Catalog reads run in one read-only transaction, so
// the checks see one snapshot and cannot write.
func (s *Service) Run(ctx context.Context, projectID string) (*Report, error) {
	doc, err := s.permissions.Document(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("read permissions: %w", err)
	}
	engine, db, err := s.open(ctx, projectID)
	if err != nil {
		return nil, err
	}
	findings, statuses, err := s.catalog(ctx, engine, db)
	if err != nil {
		return nil, err
	}
	findings = append(findings, permissionFindings(doc)...)
	statuses = append(statuses, ran(CheckAnonUnfiltered), ran(CheckWriteWithoutSelect))
	if findings == nil {
		findings = []Finding{}
	}
	sortFindings(findings)
	return &Report{Engine: engine, Findings: findings, Checks: statuses}, nil
}

func (s *Service) open(ctx context.Context, projectID string) (string, *sql.DB, error) {
	db, err := s.pools.Open(ctx, projectID)
	if errors.Is(err, domain.ErrPostgresOnly) {
		db, err = s.pools.OpenMySQL(ctx, projectID, false)
		return EngineMySQL, db, err
	}
	return EnginePostgres, db, err
}

func (s *Service) catalog(ctx context.Context, engine string, db *sql.DB) ([]Finding, []CheckStatus, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, nil, fmt.Errorf("begin read-only transaction: %w", err)
	}
	// Nothing was written; ending it is all that is left.
	defer func() { _ = tx.Rollback() }()
	if engine == EngineMySQL {
		return runMySQL(ctx, tx, s.opts)
	}
	return runPostgres(ctx, tx, s.opts)
}
