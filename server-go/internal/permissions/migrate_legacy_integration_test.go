//go:build integration

package permissions

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/schema"
	"github.com/excalibase/provisioning-poc/internal/testutil"
	"github.com/excalibase/provisioning-poc/internal/testutil/pgstore"
	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go"
	pgmod "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const tenantDDL = `
CREATE TABLE public.orders (id bigint PRIMARY KEY, owner_id uuid, email text, status text);
ALTER TABLE public.orders DROP COLUMN email;
ALTER TABLE public.orders ADD COLUMN email text;
CREATE TABLE public.notes (id int, body text);
CREATE SCHEMA auth;
CREATE TABLE auth.notes (id int, password_hash text);
CREATE VIEW public.open_orders AS SELECT id, status FROM public.orders WHERE status = 'open';

CREATE FUNCTION public.search_orders(term text, session jsonb) RETURNS SETOF public.orders
    LANGUAGE sql STABLE AS $$ SELECT * FROM public.orders WHERE status = term $$;
CREATE FUNCTION public.bump(target bigint) RETURNS public.orders
    LANGUAGE sql VOLATILE SECURITY DEFINER AS $$ UPDATE public.orders SET status = 'x' WHERE id = target RETURNING * $$;
CREATE FUNCTION public.twice(a int) RETURNS int LANGUAGE sql IMMUTABLE AS $$ SELECT a * 2 $$;
CREATE FUNCTION public.twice(a text) RETURNS text LANGUAGE sql IMMUTABLE AS $$ SELECT a || a $$;
CREATE FUNCTION public.with_out(IN a int, OUT b int, INOUT c json) LANGUAGE sql AS $$ SELECT a, c $$;
CREATE PROCEDURE public.archive() LANGUAGE sql AS $$ SELECT 1 $$;
`

// tenantDatabase starts a Postgres that plays a project's database.
func tenantDatabase(t *testing.T) *sql.DB {
	t.Helper()
	ctx := context.Background()
	password := testutil.FixturePassword("tenant-container")
	container, err := pgmod.Run(ctx, "postgres:16-alpine",
		pgmod.WithDatabase("tenant"), pgmod.WithUsername("tenant"), pgmod.WithPassword(password),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(30*time.Second)))
	if err != nil {
		t.Fatalf("start tenant postgres: %v", err)
	}
	t.Cleanup(func() { container.Terminate(ctx) })
	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "5432/tcp")
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://tenant:%s@%s:%s/tenant?sslmode=disable", password, host, port.Port()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(tenantDDL); err != nil {
		t.Fatalf("tenant ddl: %v", err)
	}
	return db
}

type onePool struct {
	projectID string
	db        *sql.DB
}

func (p onePool) Open(_ context.Context, projectID string) (*sql.DB, error) {
	if projectID != p.projectID {
		return nil, fmt.Errorf("unknown project %s", projectID)
	}
	return p.db, nil
}

func TestProjectDatabase_ReadsTheLiveSchema(t *testing.T) {
	live := NewProjectDatabase(onePool{"p1", tenantDatabase(t)}, schema.NewIntrospector())
	ctx := context.Background()

	tables, err := live.TableColumns(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(tables["public.orders"]); got != "[id owner_id status email]" {
		t.Errorf("orders columns = %s (dropped columns must not appear, order is ordinal)", got)
	}
	if got := fmt.Sprint(tables["public.open_orders"]); got != "[id status]" {
		t.Errorf("views are relations too: %s", got)
	}

	cases := []struct {
		name     string
		schema   string
		function string
		check    func(t *testing.T, details []schema.FunctionDetail)
	}{
		{"set-returning stable", "public", "search_orders", func(t *testing.T, d []schema.FunctionDetail) {
			decision, err := Trackable(d, strPtr("session"))
			if err != nil || decision.ExposedAs != domain.ExposeAsQuery || decision.SecurityDefiner {
				t.Fatalf("decision %+v err %v; details %+v", decision, err, d)
			}
		}},
		{"volatile security definer", "public", "bump", func(t *testing.T, d []schema.FunctionDetail) {
			decision, err := Trackable(d, nil)
			if err != nil || decision.ExposedAs != domain.ExposeAsMutation || !decision.SecurityDefiner {
				t.Fatalf("decision %+v err %v", decision, err)
			}
		}},
		{"overloaded", "public", "twice", func(t *testing.T, d []schema.FunctionDetail) {
			if _, err := Trackable(d, nil); err == nil || len(d) != 2 {
				t.Fatalf("overloads %d, err %v", len(d), err)
			}
		}},
		{"procedure", "public", "archive", func(t *testing.T, d []schema.FunctionDetail) {
			if _, err := Trackable(d, nil); err == nil {
				t.Fatal("a procedure must be refused")
			}
		}},
		{"out arguments are not inputs", "public", "with_out", func(t *testing.T, d []schema.FunctionDetail) {
			if len(d) != 1 || fmt.Sprint(d[0].Args) != "[{a integer} {c json}]" {
				t.Fatalf("args %+v", d)
			}
		}},
		{"any schema", "", "search_orders", func(t *testing.T, d []schema.FunctionDetail) {
			if len(d) != 1 || d[0].ReturnsTable != "public.orders" || !d[0].ReturnsSet {
				t.Fatalf("details %+v", d)
			}
		}},
		{"absent", "public", "nothing", func(t *testing.T, d []schema.FunctionDetail) {
			if _, err := Trackable(d, nil); err != ErrFunctionNotFound {
				t.Fatalf("err %v", err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			details, err := live.FunctionDetails(ctx, "p1", c.schema, c.function)
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, details)
		})
	}
}

func TestLegacyMigrator_AgainstRealDatabases(t *testing.T) {
	ctx := context.Background()
	platform := pgstore.New(t)
	tenant := tenantDatabase(t)
	const projectID = "proj-legacy"
	if err := platform.Create(&domain.DatabaseInstance{ProjectID: projectID, OrgID: "org1", Status: "ACTIVE"}); err != nil {
		t.Fatal(err)
	}
	seedLegacy(t, platform.TableGrants(), platform.RlsPolicies(), projectID)

	migrator := NewLegacyMigrator(platform.Permissions(), platform.TableGrants(), platform.RlsPolicies(),
		NewProjectDatabase(onePool{projectID, tenant}, schema.NewIntrospector()), nil)
	for run := 0; run < 2; run++ {
		migrated, err := migrator.Run(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if want := 1 - run; migrated != want {
			t.Fatalf("run %d migrated %d projects, want %d", run, migrated, want)
		}
	}

	doc, err := platform.Permissions().Document(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc)
	want := `{"projectId":"proj-legacy","version":1,"tables":[` +
		`{"table":"public.notes","role":"anon","select":{"filter":{},"columns":"*","allowAggregations":false}},` +
		`{"table":"public.orders","role":"user","select":{"filter":{"owner_id":{"_eq":"X-Excalibase-User-Id"}},"columns":["id","owner_id","status"],"allowAggregations":false},` +
		`"update":{"check":{"owner_id":{"_eq":"X-Excalibase-User-Id"}},"filter":{"owner_id":{"_eq":"X-Excalibase-User-Id"}},"columns":"*"}}],` +
		`"functions":[{"function":"public.search_orders","exposedAs":"QUERY","inferPermissions":false,"sessionArgument":null}],` +
		`"functionPermissions":[{"function":"public.search_orders","role":"user"}]}`
	var got, expected any
	_ = json.Unmarshal(raw, &got)
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	gotNorm, _ := json.Marshal(got)
	wantNorm, _ := json.Marshal(expected)
	if string(gotNorm) != string(wantNorm) {
		t.Fatalf("document\n got %s\nwant %s", gotNorm, wantNorm)
	}
	grants, err := platform.TableGrants().ListGrants(ctx, projectID)
	if err != nil || len(grants) != 3 {
		t.Fatalf("the legacy stores must stay untouched: %d grants, %v", len(grants), err)
	}
}

func seedLegacy(t *testing.T, grants interface {
	UpsertGrant(context.Context, *domain.TableGrant) error
}, policies interface {
	UpsertRls(context.Context, *domain.Policy) error
	UpsertColumn(context.Context, *domain.ColumnPolicy) error
}, projectID string) {
	t.Helper()
	ctx := context.Background()
	for _, g := range []domain.TableGrant{
		{ID: "g1", ProjectID: projectID, Resource: "notes", Role: "anon", Operations: []domain.Operation{domain.OpSelect}, Enabled: true},
		{ID: "g2", ProjectID: projectID, Resource: "public.orders", Role: "user", Operations: []domain.Operation{domain.OpSelect}, Enabled: true},
		{ID: "g3", ProjectID: projectID, Resource: "public.search_orders", Role: "user", Operations: []domain.Operation{domain.OpSelect}, Enabled: true},
	} {
		g := g
		if err := grants.UpsertGrant(ctx, &g); err != nil {
			t.Fatalf("seed grant: %v", err)
		}
	}
	owner := domain.Policy{ID: "r1", ProjectID: projectID, Name: "own rows", Resource: "orders", Effect: domain.EffectAllow,
		Operations: []domain.Operation{domain.OpSelect, domain.OpUpdate}, RuleLogic: domain.LogicAnd, Enabled: true,
		Rules:       []domain.Rule{{Field: "owner_id", FieldType: domain.FieldUUID, Operator: domain.OpEQ, Value: "{{currentUserId}}"}},
		Assignments: []domain.Assignment{{TargetType: domain.TargetRole, TargetID: "user"}}}
	if err := policies.UpsertRls(ctx, &owner); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	hide := domain.ColumnPolicy{ID: "c1", ProjectID: projectID, Name: "hide email", Resource: "orders",
		Columns: []string{"email"}, Operations: []domain.Operation{domain.OpSelect}, Mode: domain.MaskHide, Enabled: true,
		Assignments: []domain.Assignment{{TargetType: domain.TargetAll}}}
	if err := policies.UpsertColumn(ctx, &hide); err != nil {
		t.Fatalf("seed column policy: %v", err)
	}
}
