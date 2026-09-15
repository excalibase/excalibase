package edgefn

import (
	"errors"
	"sort"
	"strings"
	"testing"
)

// fakeVault mirrors the vaultclient.VaultClient interface for in-memory testing.
type fakeVault struct {
	data map[string]map[string]string
}

func newFakeVault() *fakeVault { return &fakeVault{data: map[string]map[string]string{}} }

func (f *fakeVault) Get(p string) (map[string]string, error) {
	if d, ok := f.data[p]; ok {
		return d, nil
	}
	return nil, errors.New("not found")
}
func (f *fakeVault) Put(p string, d map[string]string) error {
	// copy to avoid aliasing
	cp := make(map[string]string, len(d))
	for k, v := range d {
		cp[k] = v
	}
	f.data[p] = cp
	return nil
}
func (f *fakeVault) Delete(p string) error { delete(f.data, p); return nil }
func (f *fakeVault) DeletePrefix(prefix string) (int, error) {
	if prefix == "" {
		return 0, errors.New("empty prefix")
	}
	n := 0
	for k := range f.data {
		if strings.HasPrefix(k, prefix) {
			delete(f.data, k)
			n++
		}
	}
	return n, nil
}
func (f *fakeVault) List(prefix string) ([]string, error) {
	out := []string{}
	for k := range f.data {
		if prefix == "" || strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}
func (f *fakeVault) Sealed() bool                  { return false }
func (f *fakeVault) GetPublicKey() (string, error) { return "", nil }

func TestSecretsStore_SetAndGetAll(t *testing.T) {
	v := newFakeVault()
	s := NewSecretsStore(v)
	if err := s.Set("proj_p1", "STRIPE_KEY", "sk_test_123"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := s.GetAll("proj_p1")
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if got["STRIPE_KEY"] != "sk_test_123" {
		t.Errorf("STRIPE_KEY: got %q", got["STRIPE_KEY"])
	}
}

func TestSecretsStore_ListKeysNoValues(t *testing.T) {
	v := newFakeVault()
	s := NewSecretsStore(v)
	s.Set("proj_p1", "B_KEY", "val-b")
	s.Set("proj_p1", "A_KEY", "val-a")

	keys, err := s.ListKeys("proj_p1")
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("keys should be sorted for deterministic UI, got %v", keys)
	}
}

func TestSecretsStore_ScopedByProject(t *testing.T) {
	v := newFakeVault()
	s := NewSecretsStore(v)
	s.Set("proj_p1", "SHARED", "p1-value")
	s.Set("proj_p2", "SHARED", "p2-value")

	p1, _ := s.GetAll("proj_p1")
	p2, _ := s.GetAll("proj_p2")
	if p1["SHARED"] == p2["SHARED"] {
		t.Errorf("secrets must be scoped per project, both=%q", p1["SHARED"])
	}
	if p1["SHARED"] != "p1-value" || p2["SHARED"] != "p2-value" {
		t.Errorf("wrong values: p1=%q p2=%q", p1["SHARED"], p2["SHARED"])
	}
}

func TestSecretsStore_Delete(t *testing.T) {
	v := newFakeVault()
	s := NewSecretsStore(v)
	s.Set("proj_p1", "A_KEY", "a")
	s.Set("proj_p1", "B_KEY", "b")
	if err := s.Delete("proj_p1", "A_KEY"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, _ := s.GetAll("proj_p1")
	if _, ok := got["A_KEY"]; ok {
		t.Error("A_KEY should be gone")
	}
	if got["B_KEY"] != "b" {
		t.Errorf("B_KEY should remain: %q", got["B_KEY"])
	}
}

func TestSecretsStore_DeleteMissingIdempotent(t *testing.T) {
	v := newFakeVault()
	s := NewSecretsStore(v)
	if err := s.Delete("proj_p1", "NEVER_SET"); err != nil {
		t.Errorf("delete missing key should be idempotent, got: %v", err)
	}
}

func TestSecretsStore_RejectsInvalidKey(t *testing.T) {
	v := newFakeVault()
	s := NewSecretsStore(v)
	bad := []string{"", "has space", "lowercase-ok-too", "1LEADING_DIGIT", "with.dot", "way-too-long-way-too-long-way-too-long-way-too-long-way-too-long-way-too-long"}
	for _, k := range bad {
		if err := s.Set("proj_p1", k, "v"); err == nil {
			t.Errorf("expected error for key %q", k)
		}
	}
}

func TestSecretsStore_RejectsReservedKeys(t *testing.T) {
	v := newFakeVault()
	s := NewSecretsStore(v)
	reserved := []string{"EXCALIBASE_URL", "EXCALIBASE_PROJECT_ID", "EXCALIBASE_ANON_KEY", "EXCALIBASE_SERVICE_KEY", "EXCALIBASE_DB_URL", "EXCALIBASE_DB_HOST", "BYOC_PINNED"}
	for _, k := range reserved {
		if err := s.Set("proj_p1", k, "malicious"); err == nil {
			t.Errorf("reserved key %q should be rejected", k)
		}
	}
}

func TestSecretsStore_MaxSecretCount(t *testing.T) {
	v := newFakeVault()
	s := NewSecretsStore(v)
	for i := 0; i < MaxSecretCount; i++ {
		key := "K_"
		for j := 0; j < i/26+1; j++ {
			key += string(rune('A' + i%26))
		}
		s.Set("proj_p1", key, "v")
	}
	if err := s.Set("proj_p1", "ONE_MORE", "v"); err == nil {
		t.Error("expected error when exceeding MaxSecretCount")
	}
}

func TestSecretsStore_MaxValueLen(t *testing.T) {
	v := newFakeVault()
	s := NewSecretsStore(v)
	huge := make([]byte, MaxSecretValueLen+1)
	for i := range huge {
		huge[i] = 'x'
	}
	if err := s.Set("proj_p1", "HUGE", string(huge)); err == nil {
		t.Error("expected error for oversized secret value")
	}
}

func TestSecretsStore_BuildEnvMergesBuiltinsOverride(t *testing.T) {
	v := newFakeVault()
	s := NewSecretsStore(v)
	s.Set("proj_p1", "MY_KEY", "my-val")
	// User secret collision with a normal (non-reserved) key
	s.Set("proj_p1", "OTHER_KEY", "user-other")

	builtins := map[string]string{
		"EXCALIBASE_URL":        "https://api.excalibase.io/default/proj_p1",
		"EXCALIBASE_PROJECT_ID": "proj_p1",
		"OTHER_KEY":             "platform-override", // simulates a collision
	}
	merged, err := s.BuildEnvForDeploy("proj_p1", builtins)
	if err != nil {
		t.Fatalf("BuildEnvForDeploy: %v", err)
	}
	if merged["MY_KEY"] != "my-val" {
		t.Errorf("user key should be preserved: %q", merged["MY_KEY"])
	}
	if merged["EXCALIBASE_URL"] != builtins["EXCALIBASE_URL"] {
		t.Errorf("builtin should be injected: %q", merged["EXCALIBASE_URL"])
	}
	if merged["OTHER_KEY"] != "platform-override" {
		t.Errorf("builtin must win on collision, got: %q", merged["OTHER_KEY"])
	}
}
