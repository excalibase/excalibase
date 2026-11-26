package main

import (
	"context"
	"errors"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/orgguard"
	"github.com/excalibase/provisioning-poc/internal/trust"
)

// errNoTrustStore is what the gate reads when the platform store cannot hold
// trust decisions: with the flag on that refuses every container, never allows one.
var errNoTrustStore = errors.New("this platform store cannot read trust decisions")

type noTrustStore struct{}

func (noTrustStore) Find(context.Context, string) (*trust.Record, error) { return nil, errNoTrustStore }

func (noTrustStore) Save(context.Context, trust.Record, string) error { return errNoTrustStore }

type trustPlatform interface {
	Trust() trust.Store
	FindOrgByID(ctx context.Context, id string) (*domain.Org, error)
}

type trustProjects interface {
	FindByProjectIDContext(ctx context.Context, projectID string) (*domain.DatabaseInstance, error)
}

// newTrustGate is the container gate of the limited trial (EXC-899): it
// refuses to start a container for a project whose org owner is limited. With
// FEATURE_LIMITEDTRIAL off it allows everything and reads nothing. A platform
// admin acting through the override may still resume work, as with a suspension.
func newTrustGate(cfg config.AppConfig, projects, platform any) *trust.Gate {
	var store trust.Store = noTrustStore{}
	var orgs trust.OrgFinder = missingOrgs{}
	if p, ok := platform.(trustPlatform); ok {
		store, orgs = p.Trust(), p
	}
	var instances trust.ProjectInstances = missingProjects{}
	if p, ok := projects.(trustProjects); ok {
		instances = p
	}
	resolver := trust.NewResolver(cfg.Features(), store, instances, orgs)
	return trust.NewGate(resolver, trustPolicy(cfg)).WithBypass(orgguard.Overridden)
}

func trustPolicy(cfg config.AppConfig) trust.Policy {
	return trust.Policy{
		MinAgeDays: cfg.Trust.GitHubMinAgeDays, MinPublicEvents: cfg.Trust.GitHubMinPublicEvents,
		ActivityWindowDays: cfg.Trust.GitHubActivityWindowDays, MinRepos: cfg.Trust.GitHubMinRepos,
		RepoMinAgeDays: cfg.Trust.GitHubRepoMinAgeDays,
	}
}

type missingOrgs struct{}

func (missingOrgs) FindOrgByID(context.Context, string) (*domain.Org, error) {
	return nil, errNoTrustStore
}

type missingProjects struct{}

func (missingProjects) FindByProjectIDContext(context.Context, string) (*domain.DatabaseInstance, error) {
	return nil, errNoTrustStore
}
