package appregistry

import (
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/pkg/vault"
)

type memSecrets struct {
	data map[string]map[string]string
	err  error
}

func (m *memSecrets) Get(path string) (map[string]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	record, ok := m.data[path]
	if !ok {
		return nil, vault.ErrNotFound
	}
	return record, nil
}

func (m *memSecrets) Put(path string, record map[string]string) error {
	if m.err != nil {
		return m.err
	}
	m.data[path] = record
	return nil
}

func TestGenerations_StartAtZeroAndRotate(t *testing.T) {
	secrets := &memSecrets{data: map[string]map[string]string{}}
	generations := NewGenerations(secrets)
	if gen, err := generations.PullGeneration("proj-a"); err != nil || gen != 0 {
		t.Fatalf("a project never rotated: %d %v", gen, err)
	}
	if gen, err := generations.RotatePull("proj-a"); err != nil || gen != 1 {
		t.Fatalf("rotate: %d %v", gen, err)
	}
	if gen, _ := generations.PullGeneration("proj-a"); gen != 1 {
		t.Fatalf("after rotate: %d", gen)
	}
	if gen, _ := generations.PullGeneration("proj-b"); gen != 0 {
		t.Fatal("rotating one project moved another")
	}
	secrets.data[generationPath("proj-c")] = map[string]string{"pull-generation": "x"}
	if _, err := generations.PullGeneration("proj-c"); err == nil {
		t.Error("a damaged record is an error, never generation 0")
	}
	secrets.err = errors.New("sealed")
	if _, err := generations.PullGeneration("proj-a"); err == nil {
		t.Error("an unreadable store is an error")
	}
}

func TestPullCredential_ChangesWithTheGeneration(t *testing.T) {
	issuer, _ := testIssuer(t)
	old, rotated := issuer.PullCredential("proj-a", 0), issuer.PullCredential("proj-a", 1)
	if old == rotated || issuer.VerifyPull("proj-a", 1, old.Password) || !issuer.VerifyPull("proj-a", 1, rotated.Password) {
		t.Fatal("a rotation must retire the old password")
	}
}
