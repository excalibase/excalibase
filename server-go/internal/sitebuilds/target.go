package sitebuilds

import (
	"context"
	"errors"
	"fmt"

	"github.com/excalibase/provisioning-poc/internal/gitprovider"
)

// connected is a site's or an app's connection, as the shared git steps read it.
type connected interface {
	source() *GitSource
	target() Target
	branch() string
}

const connectorGoneReason = "the member who connected this repository can no longer deploy this project"

// checkConnector deactivates a connection whose connector lost the project or the developer rung,
// and cancels its target's waiting builds; a repository is only ever read under its connector's sign-in.
func (s *Service) checkConnector(ctx context.Context, connection connected) error {
	source := connection.source()
	if source.InactiveReason != "" {
		return ErrConnectionInactive
	}
	if s.options.Members == nil {
		return nil
	}
	standing, err := s.options.Members.Standing(ctx, source.ConnectedBy, source.ProjectID)
	if err != nil {
		return fmt.Errorf("read the connector's role: %w", err)
	}
	source.ConnectedByName = standing.Name
	if standing.CanDeploy {
		return nil
	}
	if err := s.store.DeactivateConnection(ctx, connection.target(), connectorGoneReason); err != nil {
		return err
	}
	if err := s.store.CancelBuilds(ctx, connection.target(), connectorGoneReason); err != nil {
		return err
	}
	source.InactiveReason = connectorGoneReason
	return ErrConnectionInactive
}

// mayConnect refuses a connector below the developer rung before anything is read or saved.
func (s *Service) mayConnect(ctx context.Context, userID, projectID string) error {
	if s.options.Members == nil {
		return nil
	}
	standing, err := s.options.Members.Standing(ctx, userID, projectID)
	if err != nil {
		return fmt.Errorf("read the connector's role: %w", err)
	}
	if !standing.CanDeploy {
		return ErrNotDeveloper
	}
	return nil
}

// currentRepository reads the repository's owner/name by its id, so a transfer or rename is followed
// and a stale name someone else now owns is never cloned.
func (s *Service) currentRepository(ctx context.Context, connection connected) error {
	source := connection.source()
	repository, err := s.provider.Repository(ctx, source.InstallationID, source.RepositoryID)
	if errors.Is(err, gitprovider.ErrNotFound) {
		return &InputError{Reason: "the GitHub App no longer covers " + source.Repository}
	}
	if err != nil {
		return fmt.Errorf("read the repository: %w", err)
	}
	if repository.FullName == source.Repository {
		return nil
	}
	if err := s.store.RenameRepository(ctx, connection.target(), source.RepositoryID, repository.FullName); err != nil {
		return err
	}
	source.Repository = repository.FullName
	return nil
}

// headOf reads the connected branch's head after the connector and repository checks.
func (s *Service) headOf(ctx context.Context, connection connected) (gitprovider.Commit, error) {
	if err := s.checkConnector(ctx, connection); err != nil {
		return gitprovider.Commit{}, err
	}
	if err := s.currentRepository(ctx, connection); err != nil {
		return gitprovider.Commit{}, err
	}
	source := connection.source()
	repository := gitprovider.Repository{ID: source.RepositoryID, FullName: source.Repository}
	head, err := s.provider.BranchHead(ctx, source.InstallationID, repository, connection.branch())
	if errors.Is(err, gitprovider.ErrNotFound) {
		return gitprovider.Commit{}, &InputError{Reason: fmt.Sprintf("branch %s was not found in %s", connection.branch(), source.Repository)}
	}
	if err != nil {
		return gitprovider.Commit{}, fmt.Errorf("read the branch head: %w", err)
	}
	return head, nil
}

// resolveRepository finds the repository in the user's own sign-in, then checks the App still reaches it.
func (s *Service) resolveRepository(ctx context.Context, userID string, repositoryID int64, fullName string) (int64, gitprovider.Repository, error) {
	installation, granted, err := s.grantFor(ctx, userID, repositoryID, fullName)
	if err != nil {
		return 0, gitprovider.Repository{}, err
	}
	repository, err := s.provider.Repository(ctx, installation, granted.ID)
	if errors.Is(err, gitprovider.ErrNotFound) {
		return 0, gitprovider.Repository{}, &InputError{Reason: "the GitHub App no longer covers " + granted.FullName + "; add it to the installation on GitHub"}
	}
	if err != nil {
		return 0, gitprovider.Repository{}, fmt.Errorf("read the repository: %w", err)
	}
	return installation, repository, nil
}
