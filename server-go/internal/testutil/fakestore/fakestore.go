// Package fakestore provides in-memory storage.InstanceStore, storage.OrgStore
// and auth.TokenLookup fakes shared by the authorization tests in the
// middleware, handler and cmd/server packages. They live in a non-test file
// so several test binaries can import one implementation instead of each
// carrying its own copy of the 20-method OrgStore surface.
package fakestore

import (
	"context"
	"errors"
	"time"

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

// CreateWithinOrgLimit registers the instance only while its org has a free
// slot, through the same rule the real stores apply.
func (s *Instances) CreateWithinOrgLimit(inst *domain.DatabaseInstance, maxProjects int) error {
	if s.Err != nil {
		return s.Err
	}
	if err := storage.AdmitOrgProject(s.Items, inst, maxProjects); err != nil {
		return err
	}
	s.Items[inst.ProjectID] = inst
	return nil
}

// CountOrgProjects reports how many of the org's projects hold a slot.
func (s *Instances) CountOrgProjects(orgID string) (int, error) {
	if s.Err != nil {
		return 0, s.Err
	}
	return storage.CountOrgProjectSlots(s.Items, orgID), nil
}

// Update persists changes to an existing instance, keeping its org.
func (s *Instances) Update(inst *domain.DatabaseInstance) error {
	existing, ok := s.Items[inst.ProjectID]
	if !ok {
		return storage.ErrProjectNotFound
	}
	if err := storage.CheckUpdatable(existing); err != nil {
		return err
	}
	updated := inst.Clone()
	updated.OrgID = existing.OrgID
	s.Items[inst.ProjectID] = updated
	return nil
}

// FindByProjectID returns the stored instance or nil.
// RecordRestoreInterrupted stores why a restore stopped, only on a project
// that is still being restored.
func (s *Instances) RecordRestoreInterrupted(projectID, step, reason string) error {
	existing, ok := s.Items[projectID]
	if !ok {
		return storage.ErrProjectNotFound
	}
	marked := existing.Clone()
	if err := storage.ApplyRestoreInterrupted(marked, step, reason); err != nil {
		return err
	}
	s.Items[projectID] = marked
	return nil
}

// UpdateIfStatus persists only while the row still holds expected.
func (s *Instances) UpdateIfStatus(inst *domain.DatabaseInstance, expected string) error {
	existing, ok := s.Items[inst.ProjectID]
	if !ok {
		return storage.ErrProjectNotFound
	}
	if err := storage.CheckUpdatable(existing); err != nil {
		return err
	}
	if existing.Status != expected {
		return storage.ErrProjectStatusChanged
	}
	updated := inst.Clone()
	updated.OrgID = existing.OrgID
	s.Items[inst.ProjectID] = updated
	return nil
}

// RecordPauseAttempt counts a pause about to be tried, refusing a project
// the platform may not serve.
func (s *Instances) RecordPauseAttempt(projectID string, at time.Time) (int, error) {
	existing, ok := s.Items[projectID]
	if !ok {
		return 0, storage.ErrProjectNotFound
	}
	counted := existing.Clone()
	if err := storage.ApplyPauseAttempt(counted, at); err != nil {
		return 0, err
	}
	s.Items[projectID] = counted
	return counted.PauseAttempts, nil
}

func (s *Instances) FindByProjectID(projectID string) (*domain.DatabaseInstance, error) {
	if s.Err != nil {
		return nil, s.Err
	}
	inst, ok := s.Items[projectID]
	if !ok {
		return nil, nil
	}
	return inst.Clone(), nil
}

// FindByOwner returns every instance whose OwnerID matches.
func (s *Instances) FindByOwner(ownerID string) ([]*domain.DatabaseInstance, error) {
	out := make([]*domain.DatabaseInstance, 0)
	for _, inst := range s.Items {
		if inst.OwnerID == ownerID {
			out = append(out, inst.Clone())
		}
	}
	return out, nil
}

// FindAll returns every stored instance.
func (s *Instances) FindAll() ([]*domain.DatabaseInstance, error) {
	out := make([]*domain.DatabaseInstance, 0, len(s.Items))
	for _, inst := range s.Items {
		out = append(out, inst.Clone())
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

// BeginDeletion claims the project for teardown. See storage.InstanceStore.
func (s *Instances) BeginDeletion(projectID string, deleteBackups *bool) (bool, error) {
	existing, ok := s.Items[projectID]
	if !ok {
		return false, storage.ErrProjectNotFound
	}
	claimed := existing.Clone()
	effective, err := storage.ApplyBeginDeletion(claimed, deleteBackups)
	if err != nil {
		return false, err
	}
	s.Items[projectID] = claimed
	return effective, nil
}

// RecordDeletionFailure stores how far a teardown got. See storage.InstanceStore.
func (s *Instances) RecordDeletionFailure(projectID string, status domain.ProvisioningStage, step, reason string) error {
	existing, ok := s.Items[projectID]
	if !ok {
		return storage.ErrProjectNotFound
	}
	failed := existing.Clone()
	if err := storage.ApplyDeletionFailure(failed, status, step, reason); err != nil {
		return err
	}
	s.Items[projectID] = failed
	return nil
}

// Users is a storage.UserStore holding the accounts the authorization tests
// need. Only lookups are modelled; the mutating methods answer without
// persisting, which is all a gate-level test asks of them.
type Users struct{ ByID map[string]*domain.User }

// NewUsers returns an empty user store.
func NewUsers() *Users { return &Users{ByID: map[string]*domain.User{}} }

// Add registers the user under its id.
func (s *Users) Add(u *domain.User) { s.ByID[u.ID] = u }

// FindUserByID returns the user or an error when unknown.
func (s *Users) FindUserByID(_ context.Context, id string) (*domain.User, error) {
	u := s.ByID[id]
	if u == nil {
		return nil, errors.New("user not found")
	}
	return u, nil
}

// FindUserByUsername returns the matching user or nil.
func (s *Users) FindUserByUsername(_ context.Context, username string) (*domain.User, error) {
	for _, u := range s.ByID {
		if u.Username == username {
			return u, nil
		}
	}
	return nil, nil
}

// FindUserByEmail returns the matching user or nil.
func (s *Users) FindUserByEmail(_ context.Context, email string) (*domain.User, error) {
	for _, u := range s.ByID {
		if u.Email == email {
			return u, nil
		}
	}
	return nil, nil
}

// FindAllUsers returns every registered user.
func (s *Users) FindAllUsers(context.Context) ([]*domain.User, error) {
	out := make([]*domain.User, 0, len(s.ByID))
	for _, u := range s.ByID {
		out = append(out, u)
	}
	return out, nil
}

func (s *Users) CreateUser(_ context.Context, u *domain.User) error {
	s.ByID[u.ID] = u
	return nil
}
func (s *Users) DeleteUser(_ context.Context, id string) error {
	delete(s.ByID, id)
	return nil
}
func (s *Users) UpdateUserPassword(context.Context, string, string) error { return nil }
