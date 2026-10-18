//go:build integration

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// EXC-623: apply_migration runs customer SQL as excalibase_app. On a
// database registered with the platform's role SQL, any migration that writes
// the excalibase schema fails without touching it, and one that reads the
// project's auth users is refused (EXC-628) and leaves nothing behind.
func TestApplyMigration_CannotWriteThePlatformSchema(t *testing.T) {
	c := newPlatformSchemaCase(t)
	c.register(t)
	c.seedAuthUsers(t)

	dir := t.TempDir()
	store, err := storage.NewFileSystemStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(&domain.DatabaseInstance{ProjectID: "mig-623", Namespace: "ns", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	vault := newFakeVault()
	vault.Put("projects/mig-623/credentials/excalibase_app", map[string]string{
		"host": c.pg.host, "port": c.pg.port, "username": roleApp, "password": customerAppPassword, "database": "app",
	})
	svc := NewMigrationService(store, vault, dir)
	svc.overrides = dsnOverrides{host: c.pg.host, port: c.pg.port, sslmode: "disable"}
	apply := func(name, sqlText string) *domain.MigrationRecord {
		t.Helper()
		rec, err := svc.ApplyMigration(context.Background(), "mig-623", domain.MigrationRequest{Name: name, SQL: sqlText})
		if err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		return rec
	}

	for name, sqlText := range map[string]string{
		"plant a definer function":    `CREATE FUNCTION excalibase.planted() RETURNS int LANGUAGE sql SECURITY DEFINER AS 'SELECT 1'`,
		"replace a platform function": `CREATE OR REPLACE FUNCTION excalibase.share_with_owner(target_schema oid) RETURNS void LANGUAGE sql AS ''`,
		"plant a table":               `CREATE TABLE excalibase.planted (id int)`,
		"move a table in":             `CREATE TABLE public.mover (id int); ALTER TABLE public.mover SET SCHEMA excalibase`,
		"rewrite scheduled tasks":     `UPDATE excalibase.excalibase_scheduled_functions SET status = 'pending'`,
		"take over the cron table":    `ALTER TABLE excalibase.excalibase_cron_jobs OWNER TO excalibase_app`,
		"write auth":                  `UPDATE auth.users SET email = 'mallory@example.test'`,
	} {
		rec := apply(name, sqlText)
		if rec.Status != "FAILED" {
			t.Errorf("migration %q applied", name)
			continue
		}
		if !strings.Contains(rec.ErrorMessage, "permission denied") && !strings.Contains(rec.ErrorMessage, "must be owner") {
			t.Errorf("migration %q failed for another reason: %s", name, rec.ErrorMessage)
		}
	}
	if scanBool(t, c.admin, `SELECT to_regclass('public.mover') IS NOT NULL`) {
		t.Error("a failed migration left part of its work behind")
	}

	rec := apply("profiles from auth users", `CREATE TABLE public.profiles (user_id int PRIMARY KEY, email text);
		INSERT INTO public.profiles SELECT id, email FROM auth.users`)
	if rec.Status != "FAILED" || !strings.Contains(rec.ErrorMessage, "permission denied") {
		t.Fatalf("a migration reading auth users = %s: %s", rec.Status, rec.ErrorMessage)
	}
	if scanBool(t, c.admin, `SELECT to_regclass('public.profiles') IS NOT NULL`) {
		t.Fatal("the refused auth read left its profiles table behind")
	}
	if n := c.platformObjectsNotOwnedBySuperuser(t); n != 0 {
		t.Fatalf("%d object(s) in the excalibase schema belong to a non-superuser", n)
	}
}
