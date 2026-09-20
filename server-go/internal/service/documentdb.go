package service

import (
	"context"
	"errors"
	"fmt"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/schema"
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

// documentDBStep names this step in a failed provision's recorded state.
const documentDBStep = "enable documentdb extension"

// documentDBGrantStep names the step that lets the project's own credential
// speak the MongoDB wire protocol.
const documentDBGrantStep = "grant documentdb access"

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
	return s.grantDocumentDBAccess(ctx, inst, primaryPod, pc)
}

// grantDocumentDBAccess lets the project's own credential use the MongoDB
// wire protocol.
//
// A DocumentDB project has one credential, not two. The gateway authenticates
// a Mongo client against the ordinary PostgreSQL SCRAM verifier in pg_authid,
// so any LOGIN role already authenticates over both protocols and ALTER USER
// rotates both at once. What a plain role lacks is authorisation: DocumentDB
// answers "not authorized" until the role is a member of
// documentdb_admin_role. That membership is the whole of what is needed, and
// it is one GRANT.
//
// The role granted it is the one the endpoint API advertises, so the username
// and password a customer is given work in psql and in a Mongo driver without
// their having to know the two are related.
//
// Creating the role through documentdb_api.create_user would also work, but
// it is strictly worse here: that call is not idempotent — it raises "role
// already exists" rather than converging — and all it adds beyond a plain
// CREATE ROLE is this same membership.
func (s *ProvisioningService) grantDocumentDBAccess(
	ctx context.Context, inst *domain.DatabaseInstance, primaryPod string, pc *provisioner.ProvisionContext,
) error {
	pc.SetStep(documentDBGrantStep)
	cmd := documentDBPsql(inst.DatabaseName, documentDBGrantSQL(inst.Username))
	if err := s.execRoleSQL(ctx, inst.Namespace, primaryPod, cmd); err != nil {
		return pc.Fail(fmt.Errorf("grant %s access to %s in %s: %w",
			config.DocumentDBExtension, inst.Username, inst.ProjectID, err))
	}
	return nil
}

// documentDBGrantSQL makes the project's credential a DocumentDB user. GRANT
// is idempotent, so a retried provision converges rather than failing on a
// membership it added itself a moment ago.
func documentDBGrantSQL(role string) string {
	return "GRANT " + config.DocumentDBExtension + "_admin_role TO " + schema.QuoteIdent(role)
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
