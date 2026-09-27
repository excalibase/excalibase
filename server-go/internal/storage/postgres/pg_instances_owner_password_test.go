//go:build integration

package postgres

import (
	"strings"
	"testing"
)

func TestInstances_TheOwnerPasswordNeverReachesThePlatformDatabase(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-secret01", "org-secret")
	secret := inst.Password
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	inst.Password = secret + "-rotated"
	if err := store.Update(inst); err != nil {
		t.Fatalf("Update: %v", err)
	}

	var columns int
	if err := store.DB().QueryRow(`SELECT count(*) FROM information_schema.columns
		WHERE table_name = 'database_instances' AND column_name = 'password'`).Scan(&columns); err != nil {
		t.Fatalf("columns: %v", err)
	}
	if columns != 0 {
		t.Error("database_instances still has a password column")
	}
	var row string
	if err := store.DB().QueryRow(`SELECT row_to_json(d)::text FROM database_instances d WHERE project_id = $1`,
		inst.ProjectID).Scan(&row); err != nil {
		t.Fatalf("row: %v", err)
	}
	if strings.Contains(row, secret) {
		t.Errorf("the stored row holds the owner password: %s", row)
	}
	got, err := store.FindByProjectID(inst.ProjectID)
	if err != nil || got == nil {
		t.Fatalf("FindByProjectID: %v", err)
	}
	if got.Password != "" {
		t.Errorf("a loaded row carries a password: %q", got.Password)
	}
}
