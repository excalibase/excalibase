package edgefn

import (
	"sync"
	"testing"
	"time"
)

// slowReadVault holds every read for a moment, so two writers that read before
// either writes both start from the same map.
type slowReadVault struct {
	*fakeVault
	mu sync.Mutex
}

func (v *slowReadVault) Get(p string) (map[string]string, error) {
	v.mu.Lock()
	data, err := v.fakeVault.Get(p)
	var snapshot map[string]string
	if err == nil {
		snapshot = make(map[string]string, len(data))
		for k, val := range data {
			snapshot[k] = val
		}
	}
	v.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	return snapshot, err
}

func (v *slowReadVault) Put(p string, d map[string]string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.fakeVault.Put(p, d)
}

// EXC-897: a set and a delete at once must not drop either change.
func TestSecretsStore_ConcurrentSetAndDeleteKeepBothChanges(t *testing.T) {
	const project = "proj_p1"
	for round := 0; round < 5; round++ {
		store := NewSecretsStore(&slowReadVault{fakeVault: newFakeVault()})
		if err := store.Set(project, "KEEP", "kept"); err != nil {
			t.Fatal(err)
		}
		if err := store.Set(project, "REMOVE", "gone"); err != nil {
			t.Fatal(err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		errs := make(chan error, 2)
		go func() { defer wg.Done(); errs <- store.Set(project, "ADDED", "new") }()
		go func() { defer wg.Done(); errs <- store.Delete(project, "REMOVE") }()
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}

		got, err := store.GetAll(project)
		if err != nil {
			t.Fatal(err)
		}
		if got["ADDED"] != "new" {
			t.Fatalf("round %d: the new key was lost: %v", round, got)
		}
		if _, still := got["REMOVE"]; still {
			t.Fatalf("round %d: the deleted key came back: %v", round, got)
		}
		if got["KEEP"] != "kept" {
			t.Fatalf("round %d: an untouched key was lost: %v", round, got)
		}
	}
}

func TestSecretsStore_WritersOfDifferentProjectsDoNotWait(t *testing.T) {
	store := NewSecretsStore(&slowReadVault{fakeVault: newFakeVault()})
	start := time.Now()
	var wg sync.WaitGroup
	for _, project := range []string{"proj_a", "proj_b", "proj_c", "proj_d"} {
		wg.Add(1)
		go func() { defer wg.Done(); _ = store.Set(project, "K", "v") }()
	}
	wg.Wait()
	if elapsed := time.Since(start); elapsed > 70*time.Millisecond {
		t.Fatalf("four projects took %v; they should not queue behind one another", elapsed)
	}
}
