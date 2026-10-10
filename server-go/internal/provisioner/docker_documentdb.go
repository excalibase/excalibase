package provisioner

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A DocumentDB project on a single Docker or Podman host (EXC-576) is the
// Kubernetes shape without the operator: the catalogue's DocumentDB image as
// the database container, and the upstream gateway as a second container in
// its network namespace, where it reaches Postgres on loopback as the sidecar
// does in the pod.
//
// The catalogue image is a CloudNativePG image: no entrypoint, so the
// container's command initialises the data directory itself, then runs
// postgres with DocumentDB's settings and the platform's pg_hba on the
// command line, where a restored data directory cannot override them.

const (
	// documentDBDataVolume is the image's postgres home, owned by its user,
	// so the anonymous volume there starts writable; PGDATA is beneath it.
	documentDBDataVolume = "/var/lib/postgresql"
	// DocumentDBDataDir is the data directory inside the database container.
	DocumentDBDataDir = documentDBDataVolume + "/data"
	// documentDBSocketDir is where the image's postgres puts its socket.
	documentDBSocketDir = "/var/run/postgresql"
	documentDBHBAFile   = "/tmp/excalibase_pg_hba.conf"
	// SIGINT is postgres's fast shutdown, and the gateway leaves on it; both
	// ignore or linger on the engine's default SIGTERM.
	documentDBStopSignal = "SIGINT"
)

// DocumentDBImageOwner is the uid and gid the catalogue image's postgres runs
// as; a restored data directory must belong to them.
var DocumentDBImageOwner = FileOwner{UID: 26, GID: 102}

// FileOwner is who owns the files a tar written into a container carries.
type FileOwner struct{ UID, GID int }

// DatabaseContainerName is a project's database container, Postgres or
// DocumentDB.
func DatabaseContainerName(projectID string) string {
	return fmt.Sprintf("excalibase-%s-postgres", projectID)
}

// DocumentDBGatewayName is the gateway container's name.
func DocumentDBGatewayName(projectID string) string {
	return fmt.Sprintf("excalibase-%s-documentdb", projectID)
}

// DocumentDBDatabaseSpec is the database container of a DocumentDB project,
// for a new project and a restored one alike.
func DocumentDBDatabaseSpec(projectID, major, dbName, password string, limits ContainerLimits) (ContainerSpec, error) {
	image, err := config.DockerDocumentDBImage(major)
	if err != nil {
		return ContainerSpec{}, err
	}
	return ContainerSpec{
		Name:  DatabaseContainerName(projectID),
		Image: image,
		Env: map[string]string{
			"PGDATA":            DocumentDBDataDir,
			"POSTGRES_DB":       dbName,
			"POSTGRES_PASSWORD": password,
		},
		Cmd:        []string{"bash", "-c", documentDBStartScript()},
		Ports:      map[string]string{"5432": "", strconv.Itoa(config.DocumentDBGatewayPort): ""},
		Limits:     limits,
		DataVolume: documentDBDataVolume,
		StopSignal: documentDBStopSignal,
	}, nil
}

// documentDBStartScript initialises an empty data directory the way the
// official image's entrypoint does (superuser password, the project's
// database), writes the platform's pg_hba and runs postgres.
func documentDBStartScript() string {
	return `set -eu
if [ ! -s "$PGDATA/PG_VERSION" ]; then
  pwfile=$(mktemp)
  printf '%s' "$POSTGRES_PASSWORD" > "$pwfile"
  initdb -D "$PGDATA" -U ` + defaultPostgresSuperuser + ` --pwfile="$pwfile" --auth-local=trust --auth-host=scram-sha-256 -E UTF8 --locale=C.UTF-8 >/dev/null
  rm -f "$pwfile"
  printf 'CREATE DATABASE "%s";\n' "${POSTGRES_DB//\"/\"\"}" | postgres --single -D "$PGDATA" postgres >/dev/null
fi
cat > ` + documentDBHBAFile + ` <<'HBA'
` + strings.Join(DocumentDBHBA(), "\n") + `
HBA
exec postgres -D "$PGDATA" ` + strings.Join(documentDBServerArgs(), " ")
}

// documentDBServerArgs are postgres's settings, quoted for the shell. They
// are config.DocumentDB*'s, as the Kubernetes cluster spec sets them.
func documentDBServerArgs() []string {
	settings := [][2]string{
		{"listen_addresses", "*"},
		{"hba_file", documentDBHBAFile},
		{"shared_preload_libraries", strings.Join(config.DocumentDBPreloadLibraries(), ",")},
		{config.DocumentDBCronDatabaseSetting, config.DocumentDBDatabase},
		{"cron.host", documentDBSocketDir},
		{"documentdb.localhost_connection_string", "host=" + documentDBSocketDir},
	}
	args := make([]string, 0, len(settings))
	for _, setting := range settings {
		args = append(args, "-c '"+setting[0]+"="+setting[1]+"'")
	}
	return args
}

// DocumentDBHBA is the database's pg_hba. Kubernetes' rules for the network
// and loopback; the socket, which only the container's own postgres user can
// reach (the gateway shares the network, not the filesystem), is trusted, as
// the official image trusts it.
func DocumentDBHBA() []string {
	lines := []string{"local all all trust", "local replication all trust"}
	for _, role := range []string{config.DocumentDBGatewayRole, defaultPostgresSuperuser, "+" + config.DocumentDBMongoUsersGroup} {
		lines = append(lines, "host all "+role+" 127.0.0.1/32 trust", "host all "+role+" ::1/128 trust")
	}
	return append(lines,
		"host all +"+config.DocumentDBMongoUsersGroup+" all reject",
		"host all all all scram-sha-256")
}

// bootstrapDocumentDB creates what the gateway logs in as, before it starts,
// as CloudNativePG's initdb does on Kubernetes. Idempotent.
func (p *DockerPostgreSQLProvisioner) bootstrapDocumentDB(ctx context.Context, containerID string) error {
	script := strings.Join(config.DocumentDBBootstrapSQL(), ";\n") + ";\n"
	_, err := p.docker.ExecInContainerStdin(ctx, containerID, psqlScript(config.DocumentDBDatabase), script)
	if err != nil {
		return fmt.Errorf("bootstrap %s: %w", config.DocumentDBExtension, err)
	}
	return nil
}

// psqlScript runs statements from stdin, stopping at the first error.
func psqlScript(database string) []string {
	return []string{"psql", "-U", defaultPostgresSuperuser, "-d", database, "-q", "-v", "ON_ERROR_STOP=1", "-f", "-"}
}

// documentDBReadyTimeout covers initdb on a slow disk before postgres listens.
const documentDBReadyTimeout = 3 * time.Minute

// provisionDocumentDB builds a DocumentDB project: the database, the objects
// the gateway needs, then the gateway. A failure leaves the containers for the
// provision's failure handling, as a Postgres project's is left.
func (p *DockerPostgreSQLProvisioner) provisionDocumentDB(ctx context.Context, req domain.ProvisioningRequest, tier config.TierConfig, cb StageCallback) (*ProvisioningResult, error) {
	limits, err := LimitsForTier(tier)
	if err != nil {
		return nil, err
	}
	databaseLimits, gatewayLimits := DocumentDBLimits(limits)
	dbName := databaseNameOrDefault(req.DatabaseName)
	password := generatePassword()
	spec, err := DocumentDBDatabaseSpec(req.ProjectName, req.PostgresVersion, dbName, password, databaseLimits)
	if err != nil {
		return nil, err
	}
	cb(domain.StageContainerCreation)
	containerID, err := p.docker.CreateContainerSpec(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}
	if err := p.docker.StartContainer(ctx, containerID); err != nil {
		return nil, fmt.Errorf("start container: %w", err)
	}
	cb(domain.StageWaitingForReady)
	if err := p.startDatabase(ctx, containerID, dbName, documentDBReadyTimeout); err != nil {
		return nil, err
	}
	cb(domain.StageCredentialGeneration)
	p.enableArchive(ctx, req, containerID, dbName, documentDBReadyTimeout)
	if err := p.bootstrapDocumentDB(ctx, containerID); err != nil {
		return nil, err
	}
	if err := StartDocumentDBGateway(ctx, p.docker, req.ProjectName, containerID, gatewayLimits); err != nil {
		return nil, err
	}
	cb(domain.StageCompleted)
	return &ProvisioningResult{
		Host:         spec.Name,
		Port:         5432,
		DatabaseName: dbName,
		Username:     defaultPostgresSuperuser,
		Password:     password,
		Namespace:    containerID,
		// Postgres serves no TLS here, as for a Postgres project; the gateway does.
		SSLMode: "disable",
	}, nil
}
