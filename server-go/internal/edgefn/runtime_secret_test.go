package edgefn

import "testing"

func TestDeriveRuntimeSecret(t *testing.T) {
	const master = "master-secret-value"
	a := DeriveRuntimeSecret(master, "proj-a")
	b := DeriveRuntimeSecret(master, "proj-b")

	if a == "" || b == "" {
		t.Fatal("derived secret must not be empty for a non-empty master")
	}
	if a == b {
		t.Error("different projects must derive different secrets (SEC-C5)")
	}
	if a != DeriveRuntimeSecret(master, "proj-a") {
		t.Error("derivation must be deterministic")
	}
	if a == master {
		t.Error("derived secret must not equal the master (master must never reach a pod)")
	}
	if DeriveRuntimeSecret("other-master", "proj-a") == a {
		t.Error("changing the master must change the derived secret")
	}
	if DeriveRuntimeSecret("", "proj-a") != "" {
		t.Error("empty master must derive empty (preserve fail-closed behaviour)")
	}
}
