package auth

import (
	"context"
	"fmt"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const testAdminUserID = "admin-user-id"


// mockOrgStore implements the subset of OrgStore needed for bootstrap
type mockOrgStore struct {
	orgs  []*domain.Org
	errOn string
}

func (m *mockOrgStore) FindAllOrgs(ctx context.Context) ([]*domain.Org, error) {
	if m.errOn == "FindAllOrgs" {
		return nil, fmt.Errorf("db error")
	}
	return m.orgs, nil
}

func (m *mockOrgStore) CreateOrg(ctx context.Context, org *domain.Org) error {
	if m.errOn == "CreateOrg" {
		return fmt.Errorf("db error")
	}
	m.orgs = append(m.orgs, org)
	return nil
}

func (m *mockOrgStore) AddOrgMember(ctx context.Context, member *domain.OrgMember) error {
	return nil
}

func TestBootstrapDefaultOrg_CreatesWhenEmpty(t *testing.T) {
	store := &mockOrgStore{}
	ctx := context.Background()

	err := BootstrapDefaultOrg(ctx, store, testAdminUserID)
	if err != nil {
		t.Fatalf("BootstrapDefaultOrg: %v", err)
	}

	if len(store.orgs) != 1 {
		t.Fatalf("expected 1 org, got %d", len(store.orgs))
	}

	org := store.orgs[0]
	if org.Slug != "default" {
		t.Errorf("expected slug=default, got %s", org.Slug)
	}
	if org.Name != "Default Organization" {
		t.Errorf("expected name=Default Organization, got %s", org.Name)
	}
	if org.OwnerID != testAdminUserID {
		t.Errorf("expected ownerId=admin-user-id, got %s", org.OwnerID)
	}
	if org.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestBootstrapDefaultOrg_SkipsWhenOrgsExist(t *testing.T) {
	existing := &domain.Org{ID: "org-1", Name: "Existing", Slug: "existing"}
	store := &mockOrgStore{orgs: []*domain.Org{existing}}
	ctx := context.Background()

	err := BootstrapDefaultOrg(ctx, store, testAdminUserID)
	if err != nil {
		t.Fatalf("BootstrapDefaultOrg: %v", err)
	}

	if len(store.orgs) != 1 {
		t.Errorf("should not create org when orgs exist, got %d orgs", len(store.orgs))
	}
}

func TestBootstrapDefaultOrg_ReturnsErrorOnFindAllFailure(t *testing.T) {
	store := &mockOrgStore{errOn: "FindAllOrgs"}
	err := BootstrapDefaultOrg(context.Background(), store, "admin")
	if err == nil {
		t.Error("expected error when FindAllOrgs fails")
	}
}

func TestBootstrapDefaultOrg_ReturnsErrorOnCreateFailure(t *testing.T) {
	store := &mockOrgStore{errOn: "CreateOrg"}
	err := BootstrapDefaultOrg(context.Background(), store, "admin")
	if err == nil {
		t.Error("expected error when CreateOrg fails")
	}
}
