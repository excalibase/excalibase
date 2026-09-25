//go:build integration

package docbrowser

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// The browser against a real DocumentDB gateway, as the platform's app role
// holding exactly the membership provisioning grants it. Opt-in, because it
// pulls a large image:
//
//	EXCALIBASE_DOCUMENTDB_PROTOCOL_TESTS=1 go test -tags integration ./internal/docbrowser/

const (
	protocolTestsEnv  = "EXCALIBASE_DOCUMENTDB_PROTOCOL_TESTS"
	documentDBImage   = "ghcr.io/documentdb/documentdb/documentdb-local:latest"
	localPostgresPort = "9712"
	gatewayPort       = "10260/tcp"
	appPassword       = "App-Role-Pass-9876"
)

type storeConnector struct{ store Store }

func (c storeConnector) Store(context.Context, string) (Store, error) { return c.store, nil }

func startGateway(ctx context.Context, t *testing.T) testcontainers.Container {
	t.Helper()
	if os.Getenv(protocolTestsEnv) == "" {
		t.Skipf("pulls %s; run it with %s=1", documentDBImage, protocolTestsEnv)
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        documentDBImage,
			Cmd:          []string{"--username", "bootstrap_unused", "--password", "Bootstrap-Unused-1234"},
			ExposedPorts: []string{gatewayPort},
			WaitingFor:   wait.ForLog("Gateway ready to accept connections").WithStartupTimeout(5 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start %s: %v", documentDBImage, err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(context.Background()); err != nil {
			t.Logf("terminate: %v", err)
		}
	})
	return container
}

func superuserSQL(ctx context.Context, t *testing.T, container testcontainers.Container, statement string) {
	t.Helper()
	code, _, err := container.Exec(ctx, []string{"psql", "-v", "ON_ERROR_STOP=1", "-p", localPostgresPort,
		"-U", "documentdb", "-d", "postgres", "-c", statement})
	if err != nil || code != 0 {
		t.Fatalf("SQL %q: exit %d err %v", statement, code, err)
	}
}

// appRoleClient creates excalibase_app the way provisioning does — a plain
// LOGIN role — grants it what provisioning grants, and connects as it. The
// container's certificate is self-signed for localhost, so verification is off
// here only; the connector's own TLS is covered by its unit tests.
func appRoleClient(ctx context.Context, t *testing.T, container testcontainers.Container) *mongo.Client {
	t.Helper()
	superuserSQL(ctx, t, container, "CREATE ROLE excalibase_app WITH LOGIN PASSWORD '"+appPassword+"'")
	superuserSQL(ctx, t, container, "CREATE ROLE owner_doc WITH LOGIN PASSWORD 'Owner-Pass-1234'")
	superuserSQL(ctx, t, container, `GRANT documentdb_admin_role TO "owner_doc", "excalibase_app"`)

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, gatewayPort)
	if err != nil {
		t.Fatal(err)
	}
	client, err := mongo.Connect(options.Client().
		SetHosts([]string{fmt.Sprintf("%s:%s", host, port.Port())}).
		SetDirect(true).
		SetAuth(options.Credential{AuthMechanism: authMechanism, Username: "excalibase_app", Password: appPassword}).
		SetTLSConfig(&tls.Config{InsecureSkipVerify: true}). //nolint:gosec // test container's self-signed certificate
		SetTimeout(30 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Disconnect(context.Background()); err != nil {
			t.Logf("disconnect: %v", err)
		}
	})
	return client
}

func TestBrowserAgainstARealGateway(t *testing.T) {
	ctx := context.Background()
	container := startGateway(ctx, t)
	client := appRoleClient(ctx, t, container)
	svc := NewService(storeConnector{store: &mongoStore{client: client}}, Options{Timeout: 30 * time.Second})
	ns := Namespace{Database: "shop", Collection: "orders"}

	if err := svc.CreateCollection(ctx, "p", ns); err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	seedOrders(ctx, t, svc, ns)
	assertCollectionsAndDatabases(ctx, t, svc, ns)
	assertFindAndCount(ctx, t, svc, ns)
	assertSingleDocumentWrites(ctx, t, svc, ns)
	assertIndexes(ctx, t, svc, ns)
	assertSample(ctx, t, svc, ns)

	if err := svc.DropCollection(ctx, "p", ns); err != nil {
		t.Fatalf("DropCollection: %v", err)
	}
}

func seedOrders(ctx context.Context, t *testing.T, svc *Service, ns Namespace) {
	t.Helper()
	for i := range 15 {
		body := fmt.Sprintf(`{"_id": %d, "total": %d, "customer": {"name": "c%d", "tier": "gold"}}`, i, i*10, i)
		if _, err := svc.Insert(ctx, "p", ns, []byte(body)); err != nil {
			t.Fatalf("Insert %d: %v", i, err)
		}
	}
	id, err := svc.Insert(ctx, "p", ns, []byte(`{"total": 999}`))
	if err != nil || !strings.Contains(string(id), "$oid") {
		t.Fatalf("Insert with a generated id: %s %v", id, err)
	}
	_, err = svc.Insert(ctx, "p", ns, []byte(`{"_id": 1}`))
	var queryErr *QueryError
	if !errors.As(err, &queryErr) || !queryErr.Conflict {
		t.Errorf("duplicate _id: got %v, want a conflict", err)
	}
}

func assertCollectionsAndDatabases(ctx context.Context, t *testing.T, svc *Service, ns Namespace) {
	t.Helper()
	databases, err := svc.ListDatabases(ctx, "p")
	if err != nil || !contains(databases, ns.Database) {
		t.Errorf("ListDatabases: %v %v", databases, err)
	}
	collections, err := svc.ListCollections(ctx, "p", ns.Database)
	if err != nil || len(collections) != 1 || collections[0].Name != ns.Collection {
		t.Errorf("ListCollections: %v %v", collections, err)
	}
}

func assertFindAndCount(ctx context.Context, t *testing.T, svc *Service, ns Namespace) {
	t.Helper()
	page, err := svc.Find(ctx, "p", ns, FindRequest{
		Filter: `{"total": {"$gte": 50}, "customer.tier": "gold"}`, Sort: `{"total": -1}`,
		Projection: `{"total": 1}`, Limit: 3, Skip: 1,
	})
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	got := make([]string, 0, len(page.Documents))
	for _, doc := range page.Documents {
		got = append(got, string(doc))
	}
	want := []string{canonical(13, 130), canonical(12, 120), canonical(11, 110)}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Find:\n got %v\nwant %v", got, want)
	}
	count, err := svc.Count(ctx, "p", ns, `{"customer.tier": "gold"}`)
	if err != nil || count != 15 {
		t.Errorf("Count: %d %v", count, err)
	}
	if _, err := svc.Find(ctx, "p", ns, FindRequest{Filter: `{"total": {"$nope": 1}}`}); !isQueryError(err) {
		t.Errorf("unknown operator: got %v, want a QueryError", err)
	}
}

func assertSingleDocumentWrites(ctx context.Context, t *testing.T, svc *Service, ns Namespace) {
	t.Helper()
	if err := svc.Replace(ctx, "p", ns, `3`, []byte(`{"_id": 3, "total": 31}`)); err != nil {
		t.Errorf("Replace: %v", err)
	}
	if err := svc.Update(ctx, "p", ns, `4`, []byte(`{"$set": {"total": 41}}`)); err != nil {
		t.Errorf("Update: %v", err)
	}
	if err := svc.Delete(ctx, "p", ns, `5`); err != nil {
		t.Errorf("Delete: %v", err)
	}
	if err := svc.Delete(ctx, "p", ns, `5`); !errors.Is(err, ErrDocumentNotFound) {
		t.Errorf("second delete: got %v, want ErrDocumentNotFound", err)
	}
	page, err := svc.Find(ctx, "p", ns, FindRequest{Filter: `{"_id": {"$in": [3, 4, 5]}}`, Sort: `{"_id": 1}`, Projection: `{"total": 1}`})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Documents) != 2 || string(page.Documents[0]) != canonical(3, 31) || string(page.Documents[1]) != canonical(4, 41) {
		t.Errorf("after writes: %s", page.Documents)
	}
}

func assertIndexes(ctx context.Context, t *testing.T, svc *Service, ns Namespace) {
	t.Helper()
	name, err := svc.CreateIndex(ctx, "p", ns, []byte(`{"keys": {"total": -1}, "name": "by_total", "unique": true}`))
	if err != nil || name != "by_total" {
		t.Fatalf("CreateIndex: %q %v", name, err)
	}
	indexes, err := svc.ListIndexes(ctx, "p", ns)
	if err != nil || len(indexes) != 2 {
		t.Fatalf("ListIndexes: %s %v", indexes, err)
	}
	var second struct {
		Name   string `json:"name"`
		Unique bool   `json:"unique"`
	}
	if err := json.Unmarshal(indexes[1], &second); err != nil || second.Name != "by_total" || !second.Unique {
		t.Errorf("index: %s", indexes[1])
	}
	if err := svc.DropIndex(ctx, "p", ns, "by_total"); err != nil {
		t.Errorf("DropIndex: %v", err)
	}
}

func assertSample(ctx context.Context, t *testing.T, svc *Service, ns Namespace) {
	t.Helper()
	docs, err := svc.Sample(ctx, "p", ns)
	if err != nil || len(docs) != SampleSize {
		t.Errorf("Sample: %d docs, %v", len(docs), err)
	}
}

// What provisioning grants the app role must not include managing users:
// that needs CREATEROLE, which the role does not have.
func TestTheAppRoleCannotManageUsers(t *testing.T) {
	ctx := context.Background()
	container := startGateway(ctx, t)
	client := appRoleClient(ctx, t, container)
	admin := client.Database("admin")

	commands := map[string]bson.D{
		"createUser": {{Key: "createUser", Value: "minted"}, {Key: "pwd", Value: "Minted-Pass-1234"},
			{Key: "roles", Value: bson.A{bson.D{{Key: "role", Value: "readWriteAnyDatabase"}, {Key: "db", Value: "admin"}}}}},
		"updateUser": {{Key: "updateUser", Value: "owner_doc"}, {Key: "pwd", Value: "Changed-Pass-1234"}},
		"dropUser":   {{Key: "dropUser", Value: "owner_doc"}},
	}
	for name, command := range commands {
		if err := admin.RunCommand(ctx, command).Err(); err == nil {
			t.Errorf("%s succeeded for the app role", name)
		}
	}
}

func canonical(id, total int) string {
	return fmt.Sprintf(`{"_id":{"$numberInt":"%d"},"total":{"$numberInt":"%d"}}`, id, total)
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func isQueryError(err error) bool {
	var queryErr *QueryError
	return errors.As(err, &queryErr)
}
