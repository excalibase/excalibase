package edgefn

import (
	"errors"
	"testing"
)

var errSealed = errors.New("vault is sealed")

// unreadableVault fails every read the way a sealed or unreachable vault does.
type unreadableVault struct {
	*fakeVault
	readErr error
	puts    int
}

func (u *unreadableVault) Get(string) (map[string]string, error) { return nil, u.readErr }
func (u *unreadableVault) Put(p string, d map[string]string) error {
	u.puts++
	return u.fakeVault.Put(p, d)
}

// A vault that cannot be read is not a project without secrets (EXC-447).
func TestGetAll_VaultFailureIsAnError(t *testing.T) {
	readErr := errSealed
	s := NewSecretsStore(&unreadableVault{fakeVault: newFakeVault(), readErr: readErr})
	if _, err := s.GetAll("proj_p1"); !errors.Is(err, readErr) {
		t.Fatalf("GetAll error = %v, want the vault failure", err)
	}
	if _, err := s.BuildEnvForDeploy("proj_p1", map[string]string{"A": "b"}); !errors.Is(err, readErr) {
		t.Fatalf("BuildEnvForDeploy error = %v, want the vault failure", err)
	}
}

// Set reads the whole map and writes it back: a failed read must never be
// answered with a write of one key over the others.
func TestSetAndDelete_VaultReadFailureWritesNothing(t *testing.T) {
	vault := &unreadableVault{fakeVault: newFakeVault(), readErr: errSealed}
	s := NewSecretsStore(vault)
	if err := s.Set("proj_p1", "API_KEY", "value"); err == nil {
		t.Fatal("Set succeeded although the vault could not be read")
	}
	if err := s.Delete("proj_p1", "API_KEY"); err == nil {
		t.Fatal("Delete succeeded although the vault could not be read")
	}
	if vault.puts != 0 {
		t.Fatalf("wrote %d times after a failed read, would overwrite the project's other secrets", vault.puts)
	}
}

func TestGetAll_ProjectWithoutSecretsIsEmpty(t *testing.T) {
	got, err := NewSecretsStore(newFakeVault()).GetAll("proj_new")
	if err != nil || len(got) != 0 {
		t.Fatalf("GetAll = %v, %v, want an empty map and no error", got, err)
	}
}

func TestSecretsFailuresAreMarkedUnavailable(t *testing.T) {
	failing := &unreadableVault{fakeVault: newFakeVault(), readErr: errSealed}
	if _, err := NewSecretsStore(failing).GetAll("p"); !errors.Is(err, ErrSecretsUnavailable) {
		t.Fatalf("read failure = %v, want ErrSecretsUnavailable", err)
	}
	if err := NewSecretsStore(newFakeVault()).Set("p", "bad key!", "v"); errors.Is(err, ErrSecretsUnavailable) {
		t.Fatalf("a refused key was reported as unavailable: %v", err)
	}
}

type unwritableVault struct{ *fakeVault }

func (unwritableVault) Put(string, map[string]string) error { return errors.New("connection refused") }

func TestSet_WriteFailureIsMarkedUnavailable(t *testing.T) {
	err := NewSecretsStore(unwritableVault{newFakeVault()}).Set("p", "API_KEY", "v")
	if !errors.Is(err, ErrSecretsUnavailable) {
		t.Fatalf("write failure = %v, want ErrSecretsUnavailable", err)
	}
}
