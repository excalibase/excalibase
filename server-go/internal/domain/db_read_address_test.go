package domain

import "testing"

func TestReaderRole_IsAnAllocatorRole(t *testing.T) {
	if !DBEndpointRoleReader.Valid() {
		t.Fatal("the reader role must be a holding the allocator accepts")
	}
	if DBEndpointRole("replica").Valid() {
		t.Fatal("an unknown role must still be refused")
	}
}

func TestReaderServiceName_IsDistinctPerEngine(t *testing.T) {
	postgres := DBReadAddressServiceName("proj-abcdefghij", PostgreSQL)
	mysql := DBReadAddressServiceName("proj-abcdefghij", MySQL)
	if postgres != "proj-abcdefghij-postgres-read-public" {
		t.Errorf("postgres reader service = %q", postgres)
	}
	if mysql != "proj-abcdefghij-mysql-read-public" {
		t.Errorf("mysql reader service = %q", mysql)
	}
	for _, name := range []string{postgres, mysql} {
		if name == DBEndpointServiceName("proj-abcdefghij") || len(name) > 63 {
			t.Errorf("reader service %q collides with the primary one or is too long", name)
		}
	}
}

func TestReplicasOf_CountsCopiesBesideThePrimary(t *testing.T) {
	for copies, want := range map[int64]int{0: 0, 1: 0, 3: 2, 5: 4} {
		if got := ReplicasOf(copies); got != want {
			t.Errorf("ReplicasOf(%d) = %d, want %d", copies, got, want)
		}
	}
}
