package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
)

// Turning a DocumentDB project's database into one (EXC-409).
//
// Three things have to be true before a Mongo client can be served, and the
// platform owns all three. The image carries the extension's files, which the
// catalogue records per major and provisioning refuses to promise on a major
// that cannot (EXC-407/408). The cluster preloads the extension's libraries
// and points pg_cron at the project's database, which the cluster spec does.
// This file is the third: creating the extension in that database, and
// refusing to let the project be called finished unless it is really there.
//
// It follows the rule the rest of the lifecycle keeps — nothing is recorded
// until it has been observed. A project is never reported ACTIVE with
// DocumentDB on a database where the extension is not installed; the provision
// fails instead, visibly, at this step.

// ErrDocumentDBNotInstallable is returned when the platform has no way to run
// SQL against the new database. It is a refusal rather than a skip: silently
// doing nothing would leave a project recorded as DocumentDB whose database
// has never had a statement run against it.
var ErrDocumentDBNotInstallable = errors.New("DocumentDB cannot be enabled: the platform has no way to run SQL in the project's database")

// ErrDocumentDBNotAvailableOnMajor is returned for a project that claims
// DocumentDB on a major whose image cannot carry it. Provisioning already
// refuses that combination, so reaching here means something upstream let it
// through — better a clear refusal than a psql error about a missing control
// file.
var ErrDocumentDBNotAvailableOnMajor = errors.New("DocumentDB is not available on this project's postgres major")

// ErrDocumentDBCredentialMissing is returned when the Secret the gateway
// reads its identity from is absent. The gateway cannot start without it, so
// there is nothing to be gained by inventing a password and carrying on.
var ErrDocumentDBCredentialMissing = errors.New("the project's DocumentDB credential is missing")

// documentDBStep names this step in a failed provision's recorded state.
const documentDBStep = "enable documentdb extension"

// documentDBUserStep names the step that creates the Mongo identity.
const documentDBUserStep = "create documentdb user"

// enableDocumentDB creates the DocumentDB extension in the project's own
// database and confirms it is installed. A project that was not created with
// DocumentDB is left alone entirely.
//
// The confirmation is a second statement rather than a parse of the first
// one's output, because the two exec paths differ in what they return: the
// Kubernetes one hands back stdout, the Docker one only an exit code. A
// statement that raises when the extension is absent fails both identically.
func (s *ProvisioningService) enableDocumentDB(ctx context.Context, inst *domain.DatabaseInstance, pc *provisioner.ProvisionContext) error {
	if inst == nil || !inst.DocumentDB {
		return nil
	}
	if !config.DocumentDBSupported(inst.PostgresVersion) {
		return pc.Fail(fmt.Errorf("%w: postgres %s (available on: %s)",
			ErrDocumentDBNotAvailableOnMajor, inst.PostgresVersion, config.DocumentDBMajorsMessage()))
	}
	if s.k8sClient == nil && s.dockerClient == nil {
		return pc.Fail(fmt.Errorf("%w: project %s", ErrDocumentDBNotInstallable, inst.ProjectID))
	}

	pc.SetStep(documentDBStep)
	primaryPod := inst.ProjectID + primaryPodSuffix
	for _, statement := range []string{documentDBCreateSQL(), documentDBConfirmSQL()} {
		cmd := documentDBPsql(inst.DatabaseName, statement)
		if err := s.execRoleSQL(ctx, inst.Namespace, primaryPod, cmd); err != nil {
			return pc.Fail(fmt.Errorf("enable %s in %s: %w", config.DocumentDBExtension, inst.ProjectID, err))
		}
	}
	return s.createDocumentDBUser(ctx, inst, primaryPod, pc)
}

// createDocumentDBUser registers the project's Mongo identity and files it in
// vault.
//
// The credential comes from the Secret the gateway itself reads, rather than
// being generated here: the gateway presents what is in that Secret, so
// anything else written down would be a second value that has to be kept in
// step with it. The user is created through documentdb_api.create_user, which
// registers a Mongo login as well as the Postgres role behind it — a plain
// CREATE ROLE would leave the Mongo side with nothing to authenticate.
//
// Creating it here rather than leaving it to the gateway is deliberate. The
// gateway's own setup does the same call on start-up and skips it when the
// role already exists, so doing it first makes the gateway's attempt a
// no-op — and puts the failure, if there is one, in the provision that a
// customer is watching instead of in a container log.
func (s *ProvisioningService) createDocumentDBUser(
	ctx context.Context, inst *domain.DatabaseInstance, primaryPod string, pc *provisioner.ProvisionContext,
) error {
	pc.SetStep(documentDBUserStep)
	username, password, err := s.documentDBCredential(ctx, inst)
	if err != nil {
		return pc.Fail(err)
	}
	cmd := documentDBPsql("postgres", documentDBCreateUserSQL(username, password))
	if err := s.execRoleSQL(ctx, inst.Namespace, primaryPod, cmd); err != nil {
		return pc.Fail(fmt.Errorf("create the %s user for %s: %w", config.DocumentDBExtension, inst.ProjectID, err))
	}
	return s.fileDocumentDBCredential(inst, username, password, pc)
}

// documentDBCredential reads the identity the gateway will present.
func (s *ProvisioningService) documentDBCredential(ctx context.Context, inst *domain.DatabaseInstance) (string, string, error) {
	if s.k8sClient == nil {
		return "", "", fmt.Errorf("%w: project %s has no Kubernetes Secret to read it from",
			ErrDocumentDBCredentialMissing, inst.ProjectID)
	}
	name := k8s.DocumentDBCredentialSecretName(inst.ProjectID)
	secret, err := s.k8sClient.GetSecret(ctx, inst.Namespace, name)
	if err != nil {
		return "", "", fmt.Errorf("%w: read %s/%s: %v", ErrDocumentDBCredentialMissing, inst.Namespace, name, err)
	}
	username, password := string(secret["username"]), string(secret["password"])
	if username == "" || password == "" {
		return "", "", fmt.Errorf("%w: %s/%s holds no username and password",
			ErrDocumentDBCredentialMissing, inst.Namespace, name)
	}
	return username, password, nil
}

// fileDocumentDBCredential stores the Mongo identity in the project's vault,
// beside its Postgres role credentials. A platform with no vault files
// nothing — the same condition under which no other credential is filed
// either — and the gateway still works, because it reads the Secret.
func (s *ProvisioningService) fileDocumentDBCredential(
	inst *domain.DatabaseInstance, username, password string, pc *provisioner.ProvisionContext,
) error {
	if s.vault == nil || s.vault.Sealed() {
		return nil
	}
	port := postgresPort
	if inst.Port != nil {
		port = *inst.Port
	}
	return s.putRoleCredentials(projectRoleSpec{
		projectID:    inst.ProjectID,
		namespace:    inst.Namespace,
		host:         inst.Host,
		port:         port,
		databaseName: inst.DatabaseName,
	}, roleDocumentDB, username, password, pc)
}

// documentDBCreateUserSQL registers a Mongo login through DocumentDB's own
// API, with the roles upstream's setup grants an admin user. The call takes
// one JSON document, so the values are JSON-quoted and then quoted again as a
// SQL literal; both are generated values from a Secret this platform wrote,
// never caller input.
func documentDBCreateUserSQL(username, password string) string {
	document := fmt.Sprintf(
		`{"createUser":%s,"pwd":%s,"roles":[{"role":"readWriteAnyDatabase","db":"admin"},{"role":"clusterAdmin","db":"admin"}]}`,
		strconv.Quote(username), strconv.Quote(password))
	// DO ... IF NOT EXISTS, because a retried provision must converge rather
	// than fail on a user it created itself a moment ago.
	return "DO $exc$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = " +
		sqlLiteral(username) + ") THEN PERFORM " + config.DocumentDBExtension +
		"_api.create_user(" + sqlLiteral(document) + "); END IF; END $exc$"
}

// sqlLiteral renders a single-quoted SQL string literal.
func sqlLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// documentDBPsql builds the psql invocation one statement runs under.
// ON_ERROR_STOP is what makes the exit code mean anything: without it psql
// reports a failed statement and still exits zero, so every check downstream
// would be reading a success that never happened.
func documentDBPsql(database, statement string) []string {
	return []string{"psql", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", database, "-c", statement}
}

// documentDBCreateSQL creates the extension. CASCADE brings in what it
// depends on — pg_documentdb_core, pg_cron and the contrib extensions the
// image carries — so the platform does not have to name them itself and
// cannot fall out of step with upstream when that list changes.
func documentDBCreateSQL() string {
	return "CREATE EXTENSION IF NOT EXISTS " + config.DocumentDBExtension + " CASCADE"
}

// documentDBConfirmSQL asks the database what it actually holds and raises
// when the answer is not the extension. A CREATE that returned zero is not
// evidence the extension is installed — IF NOT EXISTS succeeds against a
// database that already had it, and an exec layer that swallowed an error
// would look the same — so the project's claim rests on this read instead.
func documentDBConfirmSQL() string {
	return "DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = '" +
		config.DocumentDBExtension + "') THEN RAISE EXCEPTION " +
		"'" + config.DocumentDBExtension + " is not installed'; END IF; END $$"
}
