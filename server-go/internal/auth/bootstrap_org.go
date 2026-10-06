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
	CreateOrgWithOwner(ctx context.Context, org *domain.Org, maxFreeOrgs int) error
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

	// The admin owns it from the same transaction; the platform's own org is
	// not held to the free cap.
	if err := store.CreateOrgWithOwner(ctx, org, 0); err != nil {
		return fmt.Errorf("create default org: %w", err)
	}

	log.Printf("Created default organization (id=%s, slug=default)", org.ID)
	return nil
}
