package config

// What a DocumentDB project's database needs beyond the image carrying the
// files (EXC-409). The catalogue next door records which majors can offer
// DocumentDB at all; this file records what has to be true of a cluster and a
// database before `CREATE EXTENSION documentdb` will work in it.
//
// Everything here is upstream's, at the tag DocumentDBRef pins, and is stated
// once so the cluster spec and the SQL that runs on the new database cannot
// disagree about it.

// DocumentDBPluginName is the CNPG-I plugin that adds the gateway container
// to a DocumentDB project's Postgres pods. It is upstream's identity, not a
// name this platform chooses: the plugin answers to it, CloudNativePG
// discovers the plugin's Service by it, and a cluster asks for the sidecar by
// naming it in spec.plugins. A cluster that names it nowhere is never passed
// to the plugin at all, which is how a project without DocumentDB is left
// exactly as it was.
const DocumentDBPluginName = "cnpg-i-sidecar-injector.documentdb.io"

// DocumentDBGatewayPort is the port the gateway listens on for the MongoDB
// wire protocol inside the pod. Upstream's default, and deliberately not
// 27017: the gateway shares a pod with Postgres and the number is only ever
// dialled through a Service, so there is nothing to gain by taking the port a
// reader would expect a real mongod on.
const DocumentDBGatewayPort = 10260

// DocumentDBExtension is the extension created in DocumentDBDatabase. It is created with CASCADE, which brings in what it
// depends on — pg_documentdb_core, pg_cron and the contrib extensions the
// image carries — rather than making the platform name them one by one.
const DocumentDBExtension = "documentdb"

// DocumentDBCronDatabaseSetting is pg_cron's one-database-per-cluster setting;
// DocumentDB's DDL path runs through it, so it names DocumentDBDatabase.
const DocumentDBCronDatabaseSetting = "cron.database_name"

// DocumentDBDatabase is where the extension lives: the gateway serves only the
// postgres database, whatever the project's own database is called.
const DocumentDBDatabase = "postgres"

// DocumentDBGatewayRole is the gateway's OS user, which it logs in as over
// loopback without a password before any client authenticates.
const DocumentDBGatewayRole = "documentdb"

// DocumentDBMongoUsersGroup is the NOLOGIN group every Mongo user a project
// creates belongs to (EXC-427). pg_hba names it to trust those users on
// loopback, where the gateway connects as them, and to refuse them everywhere
// else, so they are Mongo-only.
const DocumentDBMongoUsersGroup = "excalibase_mongo_users"

// DocumentDBBrowserLogin is the Mongo user Studio's document browser logs in
// as, a member of DocumentDBMongoUsersGroup like a project's own.
const DocumentDBBrowserLogin = "excalibase_docbrowser"

// DocumentDBBootstrapSQL creates what the gateway needs the moment it starts:
// the extension it serves, then its login role, never a superuser, as a
// DocumentDB user. All are idempotent, so the provisioning step can run them again to converge. The
// block is tagged $role$, not $$: CNPG's initdb job receives $$ as $.
func DocumentDBBootstrapSQL() []string {
	return []string{
		"CREATE EXTENSION IF NOT EXISTS " + DocumentDBExtension + " CASCADE",
		"DO $role$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '" + DocumentDBGatewayRole +
			"') THEN CREATE ROLE \"" + DocumentDBGatewayRole + "\" LOGIN; END IF; END $role$",
		"GRANT " + DocumentDBExtension + "_admin_role TO \"" + DocumentDBGatewayRole + "\"",
		"DO $role$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '" + DocumentDBMongoUsersGroup +
			"') THEN CREATE ROLE \"" + DocumentDBMongoUsersGroup + "\" NOLOGIN; END IF; END $role$",
		documentDBRewindGrantSQL,
	}
}

// documentDBRewindGrantSQL lets CloudNativePG's pg_rewind, which connects as
// streaming_replica, run in a DocumentDB database (EXC-532). DocumentDB's
// hooks look up its own schemas on every statement, so without USAGE on them
// the old primary of a switchover or failover is never rewound and never
// rejoins. USAGE is lookup only: no table, sequence or function is granted.
// The role is CloudNativePG's and is skipped if it does not exist yet; the
// provisioning step runs this again once it does.
const documentDBRewindGrantSQL = "DO $role$ DECLARE s text; BEGIN " +
	"IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'streaming_replica') THEN " +
	"FOR s IN SELECT nspname FROM pg_namespace WHERE nspname LIKE 'documentdb%' LOOP " +
	"EXECUTE format('GRANT USAGE ON SCHEMA %I TO streaming_replica', s); END LOOP; END IF; END $role$"

// documentDBPreloadLibraries is the shared_preload_libraries list DocumentDB
// needs. It is upstream's own, produced by scripts/preload_libraries.sh for a
// non-distributed build (no citus, no extended rum) at the pinned tag:
//
//	echo "pg_cron, $clusterPreloadLibraries"
//
// The extension does not load without it, so a cluster that omits an entry
// does not start DocumentDB rather than starting it degraded. EXC-407's image
// test ran exactly this list against every DocumentDB-capable major and
// round-tripped a document through it.
var documentDBPreloadLibraries = []string{"pg_cron", "pg_documentdb_core", "pg_documentdb"}

// DocumentDBPreloadLibraries returns the libraries a DocumentDB cluster must
// preload, in upstream's order. The caller gets a copy: the list is read while
// a cluster spec is being built, and a caller that appended to a shared slice
// would change what every later cluster preloads.
func DocumentDBPreloadLibraries() []string {
	libraries := make([]string, len(documentDBPreloadLibraries))
	copy(libraries, documentDBPreloadLibraries)
	return libraries
}
