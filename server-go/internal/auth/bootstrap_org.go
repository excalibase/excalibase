package auth

import (
	"context"
	"fmt"
	"log"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// OrgBootstrapper is the subset of OrgStore needed for default org creation.
type OrgBootstrapper interface {
	FindAllOrgs(ctx context.Context) ([]*domain.Org, error)
	CreateOrg(ctx context.Context, org *domain.Org) error
	AddOrgMember(ctx context.Context, m *domain.OrgMember) error
}

// BootstrapDefaultOrg creates a "default" org if no orgs exist.
// Used in self-hosted mode where there's exactly one org.
func BootstrapDefaultOrg(ctx context.Context, store OrgBootstrapper, adminUserID string) error {
	orgs, err := store.FindAllOrgs(ctx)
	if err != nil {
		return fmt.Errorf("check orgs: %w", err)
	}
	if len(orgs) > 0 {
		return nil
	}

	org := &domain.Org{
		ID:      GenerateID(),
		Name:    "Default Organization",
		Slug:    "default",
		Tier:    domain.Free,
		OwnerID: adminUserID,
	}

	if err := store.CreateOrg(ctx, org); err != nil {
		return fmt.Errorf("create default org: %w", err)
	}

	// Add admin as owner
	store.AddOrgMember(ctx, &domain.OrgMember{
		OrgID:  org.ID,
		UserID: adminUserID,
		Role:   domain.OrgRoleOwner,
	})

	log.Printf("Created default organization (id=%s, slug=default)", org.ID)
	return nil
}
