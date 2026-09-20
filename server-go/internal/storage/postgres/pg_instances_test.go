//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
)

func instanceRow(projectID, orgID string) *domain.DatabaseInstance {
	port := 5432
	return &domain.DatabaseInstance{
		ProjectID: projectID, ProjectName: projectID, OrgID: orgID, OwnerID: "user-" + orgID,
		DBType: domain.PostgreSQL, Tier: domain.Free, Namespace: orgID + "-" + projectID,
		DeploymentMode: domain.ModeK8s,
		Host:           projectID + "-rw." + orgID + ".svc.cluster.local",
		Port:           &port, DatabaseName: "appdb",
		Username: projectID + "_admin", Password: projectID + "-password",
		Status: "ACTIVE",
	}
}

// Create is the only way a project row comes into existence, and a taken id
// is a conflict — never a silent rewrite of the row that holds it (EXC-415).
func TestInstances_CreateRejectsAnExistingProjectID(t *testing.T) {
	store := testStore(t)
	victim := instanceRow("proj-victim01", "org-victim")
	if err := store.Create(victim); err != nil {
		t.Fatalf("Create victim: %v", err)
	}

	err := store.Create(instanceRow("proj-victim01", "org-attacker"))
	if !errors.Is(err, storage.ErrProjectExists) {
		t.Fatalf("Create on a taken id: got %v, want ErrProjectExists", err)
	}

	got, err := store.FindByProjectID(victim.ProjectID)
	if err != nil || got == nil {
		t.Fatalf("victim row: %v", err)
	}
	if got.OrgID != "org-victim" || got.Host != victim.Host || got.Password != victim.Password {
		t.Errorf("victim row was rewritten: org=%q host=%q", got.OrgID, got.Host)
	}
}

// Update changes a project in place and can never move it to another org,
// whatever the caller puts on the struct.
func TestInstances_UpdateNeverMovesAProjectBetweenOrgs(t *testing.T) {
	store := testStore(t)
	victim := instanceRow("proj-victim02", "org-victim")
	if err := store.Create(victim); err != nil {
		t.Fatalf("Create: %v", err)
	}

	moved := instanceRow("proj-victim02", "org-attacker")
	moved.Status = "PAUSED"
	if err := store.Update(moved); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, _ := store.FindByProjectID("proj-victim02")
	if got.OrgID != "org-victim" {
		t.Errorf("org_id changed by Update: got %q, want %q", got.OrgID, "org-victim")
	}
	if got.Status != "PAUSED" {
		t.Errorf("Update must still persist mutable fields: status=%q", got.Status)
	}
}

// Updating a row that does not exist is an error, not a silent insert.
func TestInstances_UpdateMissingRowIsAnError(t *testing.T) {
	store := testStore(t)
	err := store.Update(instanceRow("proj-absent01", "org-x"))
	if !errors.Is(err, storage.ErrProjectNotFound) {
		t.Fatalf("Update on a missing row: got %v, want ErrProjectNotFound", err)
	}
	got, _ := store.FindByProjectID("proj-absent01")
	if got != nil {
		t.Error("a failed Update must not create the row")
	}
}

// The platform only operates the deployment modes it provisions. A row in any
// other mode must be refused at both ends: the schema will not accept it, and
// a row written before the constraint existed must not be read back as a
// managed instance the platform would then pause, back up or deprovision.
func TestInstance_UnsupportedDeploymentMode_IsRefused(t *testing.T) {
	store := testStore(t)

	_, err := store.DB().Exec(`
		INSERT INTO database_instances (project_id, org_id, database_type, deployment_mode, status)
		VALUES ('mode-external', 'org1', 'POSTGRESQL', 'byoc', 'ACTIVE')`)
	if err == nil {
		t.Fatal("the deployment_mode constraint must refuse a mode the platform does not operate")
	}

	if _, err := store.DB().Exec(
		`ALTER TABLE database_instances DROP CONSTRAINT database_instances_deployment_mode_check`); err != nil {
		t.Fatalf("drop constraint: %v", err)
	}
	if _, err := store.DB().Exec(`
		INSERT INTO database_instances (project_id, org_id, database_type, deployment_mode, status)
		VALUES ('mode-external', 'org1', 'POSTGRESQL', 'byoc', 'ACTIVE')`); err != nil {
		t.Fatalf("seed pre-constraint row: %v", err)
	}

	if _, err := store.FindByProjectID("mode-external"); !errors.Is(err, storage.ErrUnsupportedDeploymentMode) {
		t.Fatalf("FindByProjectID = %v, want ErrUnsupportedDeploymentMode", err)
	}
	if _, err := store.FindAll(); !errors.Is(err, storage.ErrUnsupportedDeploymentMode) {
		t.Fatalf("FindAll = %v, want ErrUnsupportedDeploymentMode", err)
	}
}

// A deleted project must take its configuration, grants and credentials with
// it: rows keyed by a project id that no longer exists would otherwise apply
// to whatever project is issued that id next.
func TestInstances_DeleteRemovesProjectOwnedRows(t *testing.T) {
	store := testStore(t)
	inst := instanceRow("proj-owned01", "org-owned")
	if err := store.Create(inst); err != nil {
		t.Fatalf("Create: %v", err)
	}
	seedProjectOwnedRows(t, store, inst.ProjectID)
	seedProjectOwnedRows(t, store, "proj-other01")

	if err := store.Delete(inst.ProjectID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, table := range projectOwnedTables {
		var left int
		if err := store.DB().QueryRow(`SELECT count(*) FROM `+table+` WHERE project_id = $1`, inst.ProjectID).Scan(&left); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if left != 0 {
			t.Errorf("%s still holds %d row(s) for the deleted project", table, left)
		}
		var others int
		if err := store.DB().QueryRow(`SELECT count(*) FROM `+table+` WHERE project_id = $1`, "proj-other01").Scan(&others); err != nil {
			t.Fatalf("count %s for other project: %v", table, err)
		}
		if others != 1 {
			t.Errorf("%s: other project's row count = %d, want 1", table, others)
		}
	}
}

// seedProjectOwnedRows puts exactly one row per project-owned table so the
// delete has something to remove in each of them.
func seedProjectOwnedRows(t *testing.T, store *Store, projectID string) {
	t.Helper()
	statements := []string{
		`INSERT INTO rls_policies (id, project_id, name, resource, effect, operations, rules, assignments) VALUES ($1||'-rls', $1, 'p', 'r', 'ALLOW', '{SELECT}', '[]'::jsonb, '[]'::jsonb)`,
		`INSERT INTO column_policies (id, project_id, name, resource, columns, operations, mode, assignments) VALUES ($1||'-col', $1, 'p', 'r', '{c}', '{SELECT}', 'HIDE', '[]'::jsonb)`,
		`INSERT INTO table_grants (id, project_id, resource, role_name, operations) VALUES ($1||'-grant', $1, 'public.r', 'anon', '{SELECT}')`,
		`INSERT INTO project_exposure_settings (project_id) VALUES ($1)`,
		`INSERT INTO project_cors_settings (project_id) VALUES ($1)`,
		`INSERT INTO edge_function_settings (project_id) VALUES ($1)`,
		`INSERT INTO edge_functions (project_id, id, doc) VALUES ($1, 'fn', '{}'::jsonb)`,
		`INSERT INTO edge_shared_files (project_id, path, content) VALUES ($1, '_shared/cors.ts', '')`,
		`INSERT INTO backup_schedules (project_id, cron_spec) VALUES ($1, '0 2 * * *')`,
		`INSERT INTO nats_credentials (principal, project_id, password_hash) VALUES ($1||'-nats', $1, 'h')`,
		`INSERT INTO storage_buckets (id, project_id, name) VALUES ($1||'-bucket', $1, 'b')`,
		`INSERT INTO storage_quota (project_id) VALUES ($1)`,
		`INSERT INTO users (id, username, email, password_hash) VALUES ($1||'-user', $1||'-user', $1||'@example.test', 'h')`,
		`INSERT INTO orgs (id, name, slug, owner_id) VALUES ($1||'-org', 'o', $1||'-org', $1||'-user')`,
		`INSERT INTO project_members (project_id, org_id, user_id, role) VALUES ($1, $1||'-org', $1||'-user', 'viewer')`,
		`INSERT INTO access_tokens (token_hash, token_prefix, user_id, name, project_id) VALUES ($1||'-tok', 'exc_', $1||'-user', 'n', $1)`,
	}
	for _, stmt := range statements {
		if _, err := store.DB().Exec(stmt, projectID); err != nil {
			t.Fatalf("seed %q: %v", stmt, err)
		}
	}
}

// EXC-419 — the storage rows go with the project's record, in the same
// transaction. Any left behind would name a project that no longer exists and
// keep counting bytes nobody can reach, so this pins the cleanup rather than
// trusting the table list to stay complete.
func TestInstances_DeleteRemovesEveryStorageRow(t *testing.T) {
	store := testStore(t)
	const projectID = "proj-storagegone"
	if err := store.Create(instanceRow(projectID, "org-storagegone")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	now := time.Now().UTC()
	if err := store.CreateBucket(context.Background(), &storagesvc.Bucket{
		ID: "bkt_gone", ProjectID: projectID, Name: "files", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateBucket: %v", err)
	}
	if err := store.CreateObject(context.Background(), &storagesvc.Object{
		ID: "obj_gone", BucketID: "bkt_gone", Key: "a.bin", Size: 10, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreateObject: %v", err)
	}
	if err := store.AddQuotaBytes(context.Background(), projectID, 10); err != nil {
		t.Fatalf("AddQuotaBytes: %v", err)
	}

	if err := store.Delete(projectID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	counts := map[string]string{
		"storage_buckets": `SELECT COUNT(*) FROM storage_buckets WHERE project_id = $1`,
		"storage_quota":   `SELECT COUNT(*) FROM storage_quota WHERE project_id = $1`,
		// Objects cascade from their bucket, so they are counted by it.
		"storage_objects": `SELECT COUNT(*) FROM storage_objects
		                    WHERE bucket_id IN (SELECT id FROM storage_buckets WHERE project_id = $1)
		                       OR bucket_id = 'bkt_gone'`,
	}
	for table, query := range counts {
		var left int
		if err := store.DB().QueryRow(query, projectID).Scan(&left); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if left != 0 {
			t.Errorf("%d %s row(s) survived the project's deletion", left, table)
		}
	}
}
