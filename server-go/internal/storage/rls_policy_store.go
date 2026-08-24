package storage

import (
	"context"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// RlsPolicyStore persists the row-level and column-level policies that
// excalibase-graphql fetches at query time. Project-scoped on every method
// — callers must always pass projectID; we never expose a tenant's
// policies to another tenant.
//
// See EXC-318 and rfc/0001-rls-engine.md.
type RlsPolicyStore interface {
	// --- row-level (RLS) policies ---

	ListRls(ctx context.Context, projectID, resource string) ([]domain.Policy, error)
	GetRls(ctx context.Context, projectID, id string) (*domain.Policy, error)
	UpsertRls(ctx context.Context, p *domain.Policy) error
	DeleteRls(ctx context.Context, projectID, id string) error

	// --- column-level (CLS) policies ---

	ListColumn(ctx context.Context, projectID, resource string) ([]domain.ColumnPolicy, error)
	GetColumn(ctx context.Context, projectID, id string) (*domain.ColumnPolicy, error)
	UpsertColumn(ctx context.Context, p *domain.ColumnPolicy) error
	DeleteColumn(ctx context.Context, projectID, id string) error
}
