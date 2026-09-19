// Package fakestore provides in-memory storage.InstanceStore, storage.OrgStore
// and auth.TokenLookup fakes shared by the authorization tests in the
// middleware, handler and cmd/server packages. They live in a non-test file
// so several test binaries can import one implementation instead of each
// carrying its own copy of the 20-method OrgStore surface.
package fakestore

import (
	"context"
	"errors"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// Instances is an InstanceStore keyed by project id. Err, when set, is
// returned from every lookup so callers can exercise the fail-closed path.
type Instances struct {
	Items map[string]*domain.DatabaseInstance
	Err   error
}

// NewInstances returns an empty instance store.
func NewInstances() *Instances {
	return &Instances{Items: map[string]*domain.DatabaseInstance{}}
}

// Create registers the instance, refusing an id that is already taken.
func (s *Instances) Create(inst *domain.DatabaseInstance) error {
	if _, taken := s.Items[inst.ProjectID]; taken {
		return storage.ErrProjectExists
	}
	s.Items[inst.ProjectID] = inst
	return nil
}

// Update persists changes to an existing instance, keeping its org.
func (s *Instances) Update(inst *domain.DatabaseInstance) error {
	existing, ok := s.Items[inst.ProjectID]
	if !ok {
		return storage.ErrProjectNotFound
	}
	updated := *inst
	updated.OrgID = existing.OrgID
	s.Items[inst.ProjectID] = &updated
	return nil
}

// FindByProjectID returns the stored instance or nil.
func (s *Instances) FindByProjectID(projectID string) (*domain.DatabaseInstance, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	return s.Items[projectID], nil
}

// FindByOwner returns every instance whose OwnerID matches.
func (s *Instances) FindByOwner(ownerID string) ([]*domain.DatabaseInstance, error) {
	out := make([]*domain.DatabaseInstance, 0)
	for _, inst := range s.Items {
		if inst.OwnerID == ownerID {
			out = append(out, inst)
		}
	}
	return out, nil
}

// FindAll returns every stored instance.
func (s *Instances) FindAll() ([]*domain.DatabaseInstance, error) {
	out := make([]*domain.DatabaseInstance, 0, len(s.Items))
	for _, inst := range s.Items {
		out = append(out, inst)
	}
	return out, nil
}

// Delete removes the instance for the project id.
func (s *Instances) Delete(projectID string) error {
	delete(s.Items, projectID)
	return nil
}

// Orgs is an OrgStore that only models org membership; every other method is
// a no-op returning zero values. Members is keyed by org id then user id.
type Orgs struct {
	Members map[string]map[string]*domain.OrgMember
	Err     error
}

// NewOrgs returns an empty org store.
func NewOrgs() *Orgs {
	return &Orgs{Members: map[string]map[string]*domain.OrgMember{}}
}

// AddMember records userID as a member of orgID with the given role.
func (s *Orgs) AddMember(orgID, userID, role string) {
	if s.Members[orgID] == nil {
		s.Members[orgID] = map[string]*domain.OrgMember{}
	}
	s.Members[orgID][userID] = &domain.OrgMember{OrgID: orgID, UserID: userID, Role: role}
}

// GetOrgMember returns the membership row or nil when absent.
func (s *Orgs) GetOrgMember(_ context.Context, orgID, userID string) (*domain.OrgMember, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	return s.Members[orgID][userID], nil
}

func (s *Orgs) CreateOrg(context.Context, *domain.Org) error                      { return nil }
func (s *Orgs) FindOrgByID(context.Context, string) (*domain.Org, error)          { return nil, nil }
func (s *Orgs) FindOrgBySlug(context.Context, string) (*domain.Org, error)        { return nil, nil }
func (s *Orgs) FindOrgsByUser(context.Context, string) ([]*domain.Org, error)     { return nil, nil }
func (s *Orgs) FindAllOrgs(context.Context) ([]*domain.Org, error)                { return nil, nil }
func (s *Orgs) UpdateOrg(context.Context, *domain.Org) error                      { return nil }
func (s *Orgs) DeleteOrg(context.Context, string) error                           { return nil }
func (s *Orgs) AddOrgMember(context.Context, *domain.OrgMember) error             { return nil }
func (s *Orgs) RemoveOrgMember(context.Context, string, string) error             { return nil }
func (s *Orgs) UpdateOrgMemberRole(context.Context, string, string, string) error { return nil }
func (s *Orgs) ListOrgMembers(context.Context, string) ([]*domain.OrgMember, error) {
	return nil, nil
}
func (s *Orgs) AddProjectMember(context.Context, *domain.ProjectMember) error { return nil }
func (s *Orgs) RemoveProjectMember(context.Context, string, string) error     { return nil }
func (s *Orgs) UpdateProjectMemberRole(context.Context, string, string, string) error {
	return nil
}
func (s *Orgs) ListProjectMembers(context.Context, string) ([]*domain.ProjectMember, error) {
	return nil, nil
}
func (s *Orgs) GetProjectMember(context.Context, string, string) (*domain.ProjectMember, error) {
	return nil, nil
}
func (s *Orgs) CreatePendingInvite(context.Context, *domain.PendingInvite) error { return nil }
func (s *Orgs) FindPendingInvitesByEmail(context.Context, string) ([]*domain.PendingInvite, error) {
	return nil, nil
}
func (s *Orgs) DeletePendingInvite(context.Context, int64) error { return nil }
func (s *Orgs) ListPendingInvites(context.Context, string) ([]*domain.PendingInvite, error) {
	return nil, nil
}

// Tokens is an auth.TokenLookup: tokens keyed by hash, users keyed by id.
type Tokens struct {
	ByHash map[string]*domain.AccessToken
	Users  map[string]*domain.User
}

// NewTokens returns an empty token/user lookup.
func NewTokens() *Tokens {
	return &Tokens{ByHash: map[string]*domain.AccessToken{}, Users: map[string]*domain.User{}}
}

// FindByTokenHash returns the token for the hash or nil.
func (s *Tokens) FindByTokenHash(_ context.Context, hash string) (*domain.AccessToken, error) {
	return s.ByHash[hash], nil
}

// FindUserByID returns the user or an error when unknown, mirroring the
// real store's not-found behaviour.
func (s *Tokens) FindUserByID(_ context.Context, id string) (*domain.User, error) {
	u := s.Users[id]
	if u == nil {
		return nil, errors.New("user not found")
	}
	return u, nil
}
