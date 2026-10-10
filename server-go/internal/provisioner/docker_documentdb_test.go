package provisioner

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

func dockerDocumentDBRequest() domain.ProvisioningRequest {
	return domain.ProvisioningRequest{ProjectName: "p1", DBType: domain.PostgreSQL, PostgresVersion: "17", DocumentDB: true}
}

func provisionDocumentDB(t *testing.T, engine *fakeEngine) *ProvisioningResult {
	t.Helper()
	result, err := NewDockerPostgreSQLProvisioner(engine).Provision(context.Background(), dockerDocumentDBRequest(), testTier, func(domain.ProvisioningStage) {})
	if err != nil {
		t.Fatalf("Provision: %v (calls %v)", err, engine.calls)
	}
	return result
}

func TestDocumentDBProjectRunsTheCatalogueImageWithItsOwnStartScript(t *testing.T) {
	engine := newFakeEngine()
	result := provisionDocumentDB(t, engine)
	spec := engine.specs["excalibase-p1-postgres"]
	want, _ := config.DockerDocumentDBImage("17")
	if spec.Image != want {
		t.Fatalf("image %q, want the catalogue's %q", spec.Image, want)
	}
	if spec.DataVolume != "/var/lib/postgresql" || spec.Env["PGDATA"] != "/var/lib/postgresql/data" {
		t.Fatalf("data volume %q PGDATA %q", spec.DataVolume, spec.Env["PGDATA"])
	}
	if spec.StopSignal != "SIGINT" || len(spec.Cmd) != 3 || spec.Cmd[0] != "bash" {
		t.Fatalf("stop signal %q cmd %v", spec.StopSignal, spec.Cmd)
	}
	if _, ok := spec.Ports["5432"]; !ok {
		t.Fatalf("postgres port not published: %v", spec.Ports)
	}
	if _, ok := spec.Ports["10260"]; !ok {
		t.Fatalf("gateway port not published from the database container: %v", spec.Ports)
	}
	if spec.Env["POSTGRES_PASSWORD"] != result.Password || result.Username != "postgres" {
		t.Fatal("the superuser password handed back must be the container's")
	}
	if result.Host != "excalibase-p1-postgres" || result.Namespace != "excalibase-p1-postgres" || result.Port != 5432 || result.SSLMode != "disable" {
		t.Fatalf("result %+v", result)
	}
}

func TestDocumentDBStartScriptSetsWhatKubernetesSets(t *testing.T) {
	script := documentDBStartScript()
	for _, want := range []string{
		"shared_preload_libraries=" + strings.Join(config.DocumentDBPreloadLibraries(), ","),
		config.DocumentDBCronDatabaseSetting + "=" + config.DocumentDBDatabase,
		"documentdb.localhost_connection_string=host=/var/run/postgresql",
		"cron.host=/var/run/postgresql",
		"hba_file=",
		"listen_addresses=*",
		`if [ ! -s "$PGDATA/PG_VERSION" ]`,
	} {
		if !strings.Contains(script, want) {
			t.Errorf("start script lacks %q", want)
		}
	}
	for _, line := range DocumentDBHBA() {
		if !strings.Contains(script, line+"\n") {
			t.Errorf("start script does not write %q", line)
		}
	}
}

func TestDocumentDBHBATrustsTheGatewayAndMongoUsersOnLoopbackOnly(t *testing.T) {
	hba := DocumentDBHBA()
	group := "+" + config.DocumentDBMongoUsersGroup
	for _, role := range []string{config.DocumentDBGatewayRole, group} {
		for _, line := range hba {
			fields := strings.Fields(line)
			if fields[0] == "host" && fields[2] == role && fields[len(fields)-1] == "trust" &&
				fields[3] != "127.0.0.1/32" && fields[3] != "::1/128" {
				t.Errorf("%s trusted beyond loopback: %q", role, line)
			}
		}
	}
	reject := slices.Index(hba, "host all "+group+" all reject")
	catchAll := slices.Index(hba, "host all all all scram-sha-256")
	if reject < 0 || catchAll < 0 || reject > catchAll {
		t.Fatalf("Mongo users must be refused over the network before the password rule: %v", hba)
	}
	for _, line := range hba {
		if strings.HasPrefix(line, "host") && strings.HasSuffix(line, " all trust") {
			t.Errorf("a network line trusts every address: %q", line)
		}
	}
}

func TestDocumentDBGatewayStartsAfterTheBootstrapInTheDatabasesNamespace(t *testing.T) {
	engine := newFakeEngine()
	provisionDocumentDB(t, engine)
	gateway := engine.specs["excalibase-p1-documentdb"]
	if gateway.Image != config.DocumentDBGatewayImage() || gateway.NetnsOf != "excalibase-p1-postgres" {
		t.Fatalf("gateway image %q netns %q", gateway.Image, gateway.NetnsOf)
	}
	if len(gateway.Ports) != 0 || gateway.DataVolume != "" {
		t.Fatalf("gateway publishes %v / mounts %q", gateway.Ports, gateway.DataVolume)
	}
	bootstrap := engine.indexOf("stdin:excalibase-p1-postgres")
	create := engine.indexOf("create:excalibase-p1-documentdb")
	copied := engine.indexOf("copy:excalibase-p1-documentdb:")
	start := engine.indexOf("start:excalibase-p1-documentdb")
	if bootstrap < 0 || !(bootstrap < create && create < copied && copied < start) {
		t.Fatalf("want bootstrap, create, certificate, start in that order: %v", engine.calls)
	}
	for _, statement := range config.DocumentDBBootstrapSQL() {
		if !strings.Contains(engine.stdin[0], statement) {
			t.Errorf("bootstrap lacks %q", statement)
		}
	}
}

func TestDocumentDBProjectSplitsItsTierBetweenDatabaseAndGateway(t *testing.T) {
	engine := newFakeEngine()
	provisionDocumentDB(t, engine)
	tier, _ := LimitsForTier(testTier)
	database, gateway := engine.specs["excalibase-p1-postgres"].Limits, engine.specs["excalibase-p1-documentdb"].Limits
	if database.MemoryBytes+gateway.MemoryBytes != tier.MemoryBytes || database.NanoCPUs+gateway.NanoCPUs != tier.NanoCPUs {
		t.Fatalf("database %+v + gateway %+v != tier %+v", database, gateway, tier)
	}
	if gateway.MemoryBytes == 0 || gateway.NanoCPUs == 0 || gateway.MemoryBytes >= database.MemoryBytes {
		t.Fatalf("gateway %+v database %+v", gateway, database)
	}
}

func TestDocumentDBGatewayCertificateVerifiesForTheNamesClientsDial(t *testing.T) {
	engine := newFakeEngine()
	provisionDocumentDB(t, engine)
	files := untar(t, engine.copied["excalibase-p1-documentdb:/home/documentdb/gateway"])
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(files["tls/ca.crt"]) {
		t.Fatal("no CA in the gateway's tls directory")
	}
	block, _ := pem.Decode(files["tls/tls.crt"])
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"127.0.0.1", "localhost", "excalibase-p1-postgres", "::1"} {
		if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: name}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "excalibase-p2-postgres"}); err == nil {
		t.Error("the certificate verifies for another project's name")
	}
	if len(files["tls/tls.key"]) == 0 {
		t.Error("no key in the gateway's tls directory")
	}
}

func untar(t *testing.T, raw []byte) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	reader := tar.NewReader(bytes.NewReader(raw))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg && (header.Uid != 1000 || header.Mode != 0o600) {
			t.Errorf("%s owned by %d mode %o, want the gateway user and 0600", header.Name, header.Uid, header.Mode)
		}
		body, _ := io.ReadAll(reader)
		files[header.Name] = body
	}
}

func TestDocumentDBOnAMajorWithoutItIsRefusedBeforeAnyContainer(t *testing.T) {
	engine := newFakeEngine()
	req := dockerDocumentDBRequest()
	req.PostgresVersion = "14"
	_, err := NewDockerPostgreSQLProvisioner(engine).Provision(context.Background(), req, testTier, func(domain.ProvisioningStage) {})
	if err == nil || len(engine.calls) != 0 {
		t.Fatalf("err %v calls %v", err, engine.calls)
	}
}

func TestDocumentDBProvisionFailsWhenTheGatewayNeverListens(t *testing.T) {
	restore := documentDBGatewayReady
	documentDBGatewayReady = 50 * time.Millisecond
	t.Cleanup(func() { documentDBGatewayReady = restore })
	engine := newFakeEngine()
	engine.execCode = func(container string, _ []string) int {
		if container == "excalibase-p1-documentdb" {
			return 1
		}
		return 0
	}
	_, err := NewDockerPostgreSQLProvisioner(engine).Provision(context.Background(), dockerDocumentDBRequest(), testTier, func(domain.ProvisioningStage) {})
	if !errors.Is(err, ErrDocumentDBGatewayNotReady) {
		t.Fatalf("err = %v", err)
	}
}

func TestDocumentDBPauseStopsTheGatewayFirstAndResumeRestartsIt(t *testing.T) {
	engine := newFakeEngine()
	result := provisionDocumentDB(t, engine)
	p := NewDockerPostgreSQLProvisioner(engine)
	engine.calls = nil
	if err := p.Pause(context.Background(), result.Namespace, "p1"); err != nil {
		t.Fatal(err)
	}
	if !(engine.indexOf("stop:excalibase-p1-documentdb") < engine.indexOf("stop:excalibase-p1-postgres")) {
		t.Fatalf("pause order: %v", engine.calls)
	}
	if stopped, err := p.WorkloadStopped(context.Background(), result.Namespace, "p1"); err != nil || !stopped {
		t.Fatalf("stopped %v err %v", stopped, err)
	}
	engine.calls = nil
	if err := p.Resume(context.Background(), result.Namespace, "p1"); err != nil {
		t.Fatal(err)
	}
	if !(engine.indexOf("start:excalibase-p1-postgres") < engine.indexOf("start:excalibase-p1-documentdb")) {
		t.Fatalf("resume order: %v", engine.calls)
	}
	if stale, err := DocumentDBGatewayStale(context.Background(), engine, "p1", result.Namespace); err != nil || stale {
		t.Fatalf("after resume the gateway is stale=%v err=%v", stale, err)
	}
}

func TestWorkloadIsNotStoppedWhileTheGatewayRuns(t *testing.T) {
	engine := newFakeEngine()
	result := provisionDocumentDB(t, engine)
	_ = engine.StopContainer(context.Background(), result.Namespace)
	stopped, err := NewDockerPostgreSQLProvisioner(engine).WorkloadStopped(context.Background(), result.Namespace, "p1")
	if err != nil || stopped {
		t.Fatalf("stopped %v err %v", stopped, err)
	}
}

func TestDocumentDBDeprovisionRemovesTheGatewayBeforeTheDatabase(t *testing.T) {
	engine := newFakeEngine()
	result := provisionDocumentDB(t, engine)
	engine.calls = nil
	if err := NewDockerPostgreSQLProvisioner(engine).Deprovision(context.Background(), result.Namespace, "p1"); err != nil {
		t.Fatal(err)
	}
	gateway, database := engine.indexOf("remove:excalibase-p1-documentdb"), engine.indexOf("remove:excalibase-p1-postgres")
	if gateway < 0 || database < 0 || gateway > database || len(engine.containers) != 0 {
		t.Fatalf("calls %v left %v", engine.calls, engine.containers)
	}
}

func TestAGatewayOlderThanItsDatabaseIsStale(t *testing.T) {
	engine := newFakeEngine()
	result := provisionDocumentDB(t, engine)
	ctx := context.Background()
	_ = engine.StopContainer(ctx, result.Namespace)
	_ = engine.StartContainer(ctx, result.Namespace)
	stale, err := DocumentDBGatewayStale(ctx, engine, "p1", result.Namespace)
	if err != nil || !stale {
		t.Fatalf("stale %v err %v", stale, err)
	}
	if err := RestartDocumentDBGateway(ctx, engine, "p1"); err != nil {
		t.Fatal(err)
	}
	if stale, _ := DocumentDBGatewayStale(ctx, engine, "p1", result.Namespace); stale {
		t.Fatal("still stale after a restart")
	}
}

// A provision that failed before recording its container id leaves the row
// with the namespace it was admitted with; deleting the project still removes
// the containers, which are named after it.
func TestDeprovisionRemovesContainersAFailedProvisionLeftBehind(t *testing.T) {
	engine := newFakeEngine()
	engine.failOn = "start:excalibase-p1-documentdb"
	if _, err := NewDockerPostgreSQLProvisioner(engine).Provision(context.Background(), dockerDocumentDBRequest(), testTier, func(domain.ProvisioningStage) {}); err == nil {
		t.Fatal("want the provision to fail")
	}
	engine.failOn = ""
	if err := NewDockerPostgreSQLProvisioner(engine).Deprovision(context.Background(), "org1-p1", "p1"); err != nil {
		t.Fatal(err)
	}
	if len(engine.containers) != 0 {
		t.Fatalf("left behind: %v", engine.containers)
	}
}
