package edgefn

import (
	"testing"
	"time"
)

func TestProjectSecretsLockIsSharedAcrossStores(t *testing.T) {
	first := NewSecretsStore(newFakeVault())
	second := NewSecretsStore(newFakeVault())
	unlock := LockProjectSecrets("proj_p1")
	done := make(chan struct{})
	go func() {
		_ = second.Set("proj_p1", "KEY", "value")
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("a second store wrote while the project's secrets were locked")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the write never ran once the lock was free")
	}
	_ = first
}

func TestFunctionSecretsPathNamesItsProject(t *testing.T) {
	for path, want := range map[string]string{
		"projects/p1/edgefn/secrets":      "p1",
		"projects/p1/edgefn/secrets/":     "",
		"projects/p1/credentials/app":     "",
		"projects/p1/edgefn/secrets/more": "",
		"projects//edgefn/secrets":        "",
		"other/p1/edgefn/secrets":         "",
	} {
		if got, _ := FunctionSecretsProject(path); got != want {
			t.Errorf("%q names %q, want %q", path, got, want)
		}
	}
}
