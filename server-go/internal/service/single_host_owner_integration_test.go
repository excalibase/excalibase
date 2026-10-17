//go:build integration

package service

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// EXC-604: the login a single host hands an app (DATABASE_URL) is the
// restricted project owner, as on Kubernetes, and reaches no platform table.
func TestSingleHostAppLoginIsTheRestrictedOwner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	docker, err := provisioner.NewRealDockerClient(provisioner.DockerClientOptions{Host: os.Getenv("DOCKER_HOST")})
	if err != nil {
		t.Skipf("no docker daemon: %v", err)
	}
	defer docker.Close()

	project := fmt.Sprintf("exc604-%d", time.Now().UnixNano()%100000)
	result, err := provisioner.NewDockerPostgreSQLProvisioner(docker).Provision(ctx, domain.ProvisioningRequest{
		ProjectName: project, DBType: domain.PostgreSQL, PostgresVersion: "17",
	}, config.TierConfig{Memory: "512Mi", CPU: "0.5"}, func(domain.ProvisioningStage) {})
	t.Cleanup(func() { _ = docker.RemoveContainer(context.Background(), provisioner.DatabaseContainerName(project)) })
	if err != nil {
		t.Fatalf("provision: %v", err)
	}

	spec := projectRoleSpec{databaseName: result.DatabaseName, adminUsername: result.Username}
	creds := projectRoleCredentials{authPassword: "Auth-Password-1", appPassword: "App-Password-1", watcherPassword: "Watcher-Password-1", schedulerPassword: "Scheduler-Password-1"}
	superuser := []string{"psql", "-U", "postgres", "-d", result.DatabaseName, "-q", "-v", "ON_ERROR_STOP=1", "-f", "-"}
	if _, err := docker.ExecInContainerStdin(ctx, result.Namespace, superuser, projectRoleSQL(spec, creds, "cdc_watcher_pub")); err != nil {
		t.Fatalf("register roles: %v", err)
	}
	// A platform table in the reserved schema is the superuser's (EXC-623).
	migration := "CREATE TABLE excalibase.permissions (id int);\n" +
		"SET ROLE excalibase_app;\n" +
		"CREATE TABLE public.orders (id serial PRIMARY KEY, item text);\n" +
		"CREATE FUNCTION public.steal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$;\n"
	if _, err := docker.ExecInContainerStdin(ctx, result.Namespace, superuser, migration); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	app := connectSingleHost(ctx, t, docker, result)
	if scanBool(t, app, "SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user") {
		t.Fatal("the app login is a superuser")
	}
	mustExec(t, app, "INSERT INTO public.orders (item) VALUES ('book')")
	if got := scanString(t, app, "SELECT item FROM public.orders"); got != "book" {
		t.Fatalf("app read %q", got)
	}
	mustFail(t, app, "SELECT * FROM excalibase.permissions", "permission denied")
	mustFail(t, app, "CREATE TABLE excalibase.planted (id int)", "permission denied")
	mustFail(t, app, "CREATE TABLE auth.planted (id int)", "permission denied")
	mustFail(t, app, "CREATE TRIGGER steal BEFORE INSERT ON public.orders FOR EACH ROW EXECUTE FUNCTION public.steal()", "")
	mustFail(t, app, "ALTER ROLE excalibase_app PASSWORD 'x'", "")

	superuserLogin := *result
	superuserLogin.Username, superuserLogin.Password = "postgres", "any-password"
	if err := openSingleHost(ctx, t, docker, &superuserLogin).PingContext(ctx); err == nil || !strings.Contains(err.Error(), "rejects connection") {
		t.Fatalf("the superuser is not rejected over the network: %v", err)
	}
}

func connectSingleHost(ctx context.Context, t *testing.T, docker *provisioner.RealDockerClient, result *provisioner.ProvisioningResult) *sql.DB {
	t.Helper()
	db := openSingleHost(ctx, t, docker, result)
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("app login does not open the database: %v", err)
	}
	return db
}

func openSingleHost(ctx context.Context, t *testing.T, docker *provisioner.RealDockerClient, result *provisioner.ProvisioningResult) *sql.DB {
	t.Helper()
	state, err := docker.ContainerState(ctx, result.Namespace)
	if err != nil {
		t.Fatal(err)
	}
	port := state.HostPorts["5432"]
	if port == 0 {
		t.Fatalf("postgres not published: %+v", state.HostPorts)
	}
	dsn := url.URL{
		Scheme: "postgres", User: url.UserPassword(result.Username, result.Password),
		Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Path: "/" + result.DatabaseName,
		RawQuery: "sslmode=disable",
	}
	db, err := sql.Open("postgres", dsn.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
