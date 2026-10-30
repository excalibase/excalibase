package appregistry

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/excalibase/provisioning-poc/pkg/vault"
)

const generationKey = "pull-generation"

// SecretStore is the part of the vault the generations live in.
type SecretStore interface {
	Get(path string) (map[string]string, error)
	Put(path string, data map[string]string) error
}

// Generations is each project's pull-password generation: bumping it retires
// one project's leaked pull secret without touching any other project.
type Generations struct {
	secrets SecretStore
}

func NewGenerations(secrets SecretStore) *Generations {
	return &Generations{secrets: secrets}
}

func generationPath(projectID string) string {
	return "projects/" + projectID + "/internal-registry"
}

// PullGeneration is 0 for a project never rotated; a store that cannot say is an error.
func (g *Generations) PullGeneration(projectID string) (int, error) {
	record, err := g.secrets.Get(generationPath(projectID))
	if errors.Is(err, vault.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read the pull generation: %w", err)
	}
	gen, err := strconv.Atoi(record[generationKey])
	if err != nil || gen < 0 {
		return 0, errors.New("the stored pull generation is damaged")
	}
	return gen, nil
}

// RotatePull moves the project to a new pull password and answers its generation.
func (g *Generations) RotatePull(projectID string) (int, error) {
	gen, err := g.PullGeneration(projectID)
	if err != nil {
		return 0, err
	}
	next := gen + 1
	if err := g.secrets.Put(generationPath(projectID), map[string]string{generationKey: strconv.Itoa(next)}); err != nil {
		return 0, fmt.Errorf("store the pull generation: %w", err)
	}
	return next, nil
}
