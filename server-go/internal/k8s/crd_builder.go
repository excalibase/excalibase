package k8s

import (
	"errors"
	"maps"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	cnpgGroup      = "postgresql.cnpg.io"
	cnpgAPIVersion = "postgresql.cnpg.io/v1"
	postgresSuffix = "-postgres"
)

// GVRs for CloudNativePG CRDs.
var (
	CNPGClusterGVR = schema.GroupVersionResource{
		Group:    cnpgGroup,
		Version:  "v1",
		Resource: "clusters",
	}
	CNPGScheduledBackupGVR = schema.GroupVersionResource{
		Group:    cnpgGroup,
		Version:  "v1",
		Resource: "scheduledbackups",
	}
	CNPGBackupGVR = schema.GroupVersionResource{
		Group:    cnpgGroup,
		Version:  "v1",
		Resource: "backups",
	}
)

type PostgreSQLClusterOpts struct {
	ProjectID    string
	Namespace    string
	Tier         config.TierConfig
	Backup       *BackupOpts
	StorageClass string
	// ImageName is the digest-pinned image resolved from the catalogue by
	// the caller. The builder never derives one from a version string: that
	// would produce a floating tag.
	ImageName      string
	DatabaseName   string
	MasterUsername string
	Parameters     map[string]string
	Tags           map[string]string
	// DocumentDB configures the cluster so the DocumentDB extension can be
	// created in its database: the libraries the extension needs preloaded,
	// and pg_cron pointed at the database it will live in. It is a
	// create-time choice — a cluster's preloaded libraries are decided when
	// it is provisioned — and the caller has already checked the major can
	// offer it (EXC-408).
	DocumentDB bool
	// DocumentDBGatewayImage is the digest-pinned gateway image the CNPG-I
	// sidecar injector must use for this cluster. Named on the cluster rather
	// than left to the plugin's own default so what a tenant runs is recorded
	// on the object that runs it, and does not change when the plugin is
	// upgraded. Empty means the platform has pinned no image, and the plugin
	// is then not registered at all.
	DocumentDBGatewayImage string
	// ServerAltDNSNames are extra names the operator-issued server
	// certificate carries, so a client verifying the public name succeeds.
	ServerAltDNSNames []string
	// AllowPlaintext renders network logins as host rather than hostssl. The
	// zero value requires TLS (EXC-410).
	AllowPlaintext bool
}

// BackupOpts turns a cluster's backups on. The cluster only names its
// ObjectStore; the store itself is BuildBackupObjectStore's, from the same
// options.
type BackupOpts struct {
	Schedule      string
	RetentionDays int
	// EndpointURL is the S3-compatible endpoint. Empty means AWS S3.
	EndpointURL string
	// Bucket is required; objects land under s3://<bucket>/<projectID>/.
	Bucket string
	// SecretName is the Secret holding ACCESS_KEY_ID + ACCESS_SECRET_KEY.
	SecretName string
}

// ObjectStoreOpts locates the S3-compatible store a CNPG cluster reads
// from or writes to. Backup and restore share it so a restore always
// targets the store the backup landed in.
type ObjectStoreOpts struct {
	EndpointURL string // empty → omitted, Barman defaults to AWS S3
	Bucket      string
	SecretName  string // K8s Secret holding ACCESS_KEY_ID + ACCESS_SECRET_KEY
}

// RestoreClusterOpts describes a CNPG cluster bootstrapped by recovery
// from another project's Barman object store.
type RestoreClusterOpts struct {
	// Cluster is the restored project's cluster exactly as a new project with
	// the same settings would get it; only its bootstrap is replaced.
	Cluster         PostgreSQLClusterOpts
	SourceProjectID string
	Store           ObjectStoreOpts
	// RecoveryTarget is the optional PITR target (targetTime / targetXID /
	// targetLSN / targetName) in CNPG's recoveryTarget shape.
	RecoveryTarget map[string]interface{}
}

// ErrTierSizingIncomplete refuses to render a cluster whose tier does not say
// how large it is or what it may use.
var ErrTierSizingIncomplete = errors.New("tier does not define the cluster's instances, storage, cpu and memory")

func validateTierSizing(tier config.TierConfig) error {
	if tier.Instances < 1 || tier.StorageSize == "" || tier.CPU == "" || tier.Memory == "" {
		return ErrTierSizingIncomplete
	}
	return nil
}

// applyTierSizing is the one place a tier becomes a cluster's instance count,
// volume and CPU/memory, so a new and a restored project of the same tier
// cannot drift apart.
func applyTierSizing(spec map[string]interface{}, tier config.TierConfig, storageClass string) {
	storage := map[string]interface{}{"size": tier.StorageSize}
	if storageClass != "" {
		storage["storageClass"] = storageClass
	}
	spec["instances"] = int64(tier.Instances)
	spec["storage"] = storage
	spec["resources"] = map[string]interface{}{
		"requests": tierBounds(tier),
		"limits":   tierBounds(tier),
	}
	if tier.Instances > 1 {
		spec["affinity"] = oneInstancePerNode()
	}
	applyUpdatePolicy(spec, tier.Instances)
	applyReplicationGuarantee(spec, tier.Instances)
}

// applyReplicationGuarantee makes a multi-instance cluster acknowledge a
// commit only once one standby has it, so losing the primary loses no
// acknowledged write (owner decision 2026-10-01, EXC-532). Required, not
// preferred: with no standby up, writes wait instead of going ahead
// unprotected. A single instance has no standby to wait for.
func applyReplicationGuarantee(spec map[string]interface{}, instances int) {
	postgresql, _ := spec["postgresql"].(map[string]interface{})
	if postgresql == nil {
		postgresql = map[string]interface{}{}
		spec["postgresql"] = postgresql
	}
	if instances < 2 {
		delete(postgresql, "synchronous")
		return
	}
	postgresql["synchronous"] = map[string]interface{}{
		"method":         "any",
		"number":         int64(1),
		"dataDurability": "required",
	}
}

// applyUpdatePolicy decides how an image update (a minor upgrade) reaches the
// primary once the standbys run it. With standbys the operator switches over
// to an updated one, so clients only reconnect; a single instance has nothing
// to switch to and restarts in place, which is a brief outage (EXC-493).
// Unsupervised: the operator finishes the update without a manual step.
func applyUpdatePolicy(spec map[string]interface{}, instances int) {
	spec["primaryUpdateStrategy"] = "unsupervised"
	if instances > 1 {
		spec["primaryUpdateMethod"] = "switchover"
		return
	}
	spec["primaryUpdateMethod"] = "restart"
}

// oneInstancePerNode makes a node loss cost at most one instance. Required,
// not preferred: an instance that cannot get its own node stays Pending
// rather than sharing one, and admission refuses a platform with too few
// nodes before the cluster is created.
func oneInstancePerNode() map[string]interface{} {
	return map[string]interface{}{
		"enablePodAntiAffinity": true,
		"podAntiAffinityType":   "required",
		"topologyKey":           "kubernetes.io/hostname",
	}
}

func tierBounds(tier config.TierConfig) map[string]interface{} {
	return map[string]interface{}{"memory": tier.Memory, "cpu": tier.CPU}
}

// tierQueryGuard cancels queries and idle-in-transaction sessions past the
// tier's budget so one tenant can't peg a shared box with a never-ending query.
func tierQueryGuard(tier config.TierConfig) map[string]interface{} {
	params := map[string]interface{}{}
	if tier.StatementTimeout != "" {
		params["statement_timeout"] = tier.StatementTimeout
		params["idle_in_transaction_session_timeout"] = tier.StatementTimeout
	}
	return params
}

// BuildPostgreSQLCluster builds a CloudNativePG Cluster CRD as unstructured.
func BuildPostgreSQLCluster(opts PostgreSQLClusterOpts) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": cnpgAPIVersion,
			"kind":       "Cluster",
			"metadata":   buildClusterMetadata(opts),
			"spec":       buildClusterSpec(opts),
		},
	}
}

// buildClusterMetadata builds the metadata section of a CNPG Cluster CRD.
func buildClusterMetadata(opts PostgreSQLClusterOpts) map[string]interface{} {
	labels := map[string]interface{}{}
	for k, v := range opts.Tags {
		labels[k] = v
	}

	metadata := map[string]interface{}{
		"name":      opts.ProjectID + postgresSuffix,
		"namespace": opts.Namespace,
	}
	if len(labels) > 0 {
		metadata["labels"] = labels
	}
	if opts.Backup != nil {
		metadata["annotations"] = map[string]interface{}{SkipEmptyWalArchiveCheckAnnotation: "enabled"}
	}
	return metadata
}

// defaultClusterDatabase and defaultClusterUser are what CNPG bootstraps when
// the request names neither. They are named here rather than repeated because
// the DocumentDB configuration has to point pg_cron at the same database the
// cluster actually creates.
const (
	defaultClusterDatabase = "app"
	defaultClusterUser     = "app"
	// appRoleName is the platform's own login role in every project database.
	appRoleName = "excalibase_app"
)

// clusterDatabaseName is the database CNPG bootstraps for this project.
func clusterDatabaseName(opts PostgreSQLClusterOpts) string {
	if opts.DatabaseName != "" {
		return opts.DatabaseName
	}
	return defaultClusterDatabase
}

func clusterOwner(opts PostgreSQLClusterOpts) string {
	if opts.MasterUsername != "" {
		return opts.MasterUsername
	}
	return defaultClusterUser
}

// hbaSafeOwner is the owner as a pg_hba/pg_ident role, or nothing when the
// name is not a plain role name: the request boundary refuses those, and a
// trust line is never written for one that slipped past it (EXC-555).
func hbaSafeOwner(opts PostgreSQLClusterOpts) []string {
	owner := clusterOwner(opts)
	if domain.ValidateMasterUsername(owner) != nil {
		return nil
	}
	return []string{owner}
}

// documentDBLoopbackTrust lets the gateway, which only does passwordless local
// logins, reach Postgres as itself and as the roles clients authenticate as.
// Loopback only: nothing outside the pod matches these lines. The project's
// own Mongo users are then refused every other login (EXC-427).
func documentDBLoopbackTrust(opts PostgreSQLClusterOpts) []interface{} {
	mongoUsers := "+" + config.DocumentDBMongoUsersGroup
	// No platform role: the gateway would open a session for its password (EXC-410).
	roles := append(append([]string{config.DocumentDBGatewayRole}, hbaSafeOwner(opts)...), mongoUsers)
	lines := make([]interface{}, 0, 2*len(roles)+1)
	for _, role := range roles {
		lines = append(lines,
			"host all "+role+" 127.0.0.1/32 trust",
			"host all "+role+" ::1/128 trust")
	}
	return append(lines, mongoUsersNetworkReject)
}

// mongoUsersNetworkReject is "host", matching TLS and plaintext alike.
var mongoUsersNetworkReject = "host all +" + config.DocumentDBMongoUsersGroup + " all reject"

// CNPG fixes unix_socket_directories here; local connections get its peer mapping.
const cnpgSocketDirectory = "/controller/run"

// documentDBLocalhostSetting is how the extension and its background worker connect back to Postgres.
const documentDBLocalhostSetting = "documentdb.localhost_connection_string"

// The gateway sidecar ignores SIGTERM, so without these a pod holds CNPG's
// 1800s grace period on every delete, pause and rollout.
const (
	documentDBStopDelaySeconds     = 60
	documentDBSmartShutdownSeconds = 15
)

// documentDBPeerIdentities lets the postgres OS user, which already maps to
// the superuser, connect over the socket as the roles DocumentDB's own
// connect-backs use: its background worker, and the roles clients act as.
func documentDBPeerIdentities(opts PostgreSQLClusterOpts) []interface{} {
	roles := append([]string{"documentdb_bg_worker_role", config.DocumentDBGatewayRole}, hbaSafeOwner(opts)...)
	lines := make([]interface{}, 0, len(roles))
	for _, role := range roles {
		lines = append(lines, "local postgres "+role)
	}
	// A project's own Mongo users are added one line each (WithMongoUserIdent).
	return lines
}

// serverAltDNSNames adds the gateway Service's names for a DocumentDB project,
// whose gateway presents this same certificate.
func serverAltDNSNames(opts PostgreSQLClusterOpts) []string {
	names := append([]string(nil), opts.ServerAltDNSNames...)
	if !opts.DocumentDB {
		return names
	}
	service := DocumentDBServiceName(opts.ProjectID)
	return append(names,
		service,
		service+"."+opts.Namespace,
		service+"."+opts.Namespace+".svc",
		service+"."+opts.Namespace+".svc.cluster.local")
}

// namedAppDatabase is the database and owner a bootstrap names, or nil when
// they are CNPG's own defaults.
func namedAppDatabase(opts PostgreSQLClusterOpts) map[string]interface{} {
	dbName, dbUser := clusterDatabaseName(opts), clusterOwner(opts)
	if dbName == defaultClusterDatabase && dbUser == defaultClusterUser {
		return nil
	}
	return map[string]interface{}{"database": dbName, "owner": dbUser}
}

// buildClusterSpec builds the spec section of a CNPG Cluster CRD.
func buildClusterSpec(opts PostgreSQLClusterOpts) map[string]interface{} {
	postgresql := buildPostgresql(opts)

	// enablePodMonitor is left off entirely. CNPG's PodMonitor reconciler
	// blows up with "cannot create Cluster auxiliary objects: expected
	// pointer, but got invalid kind" when the Prometheus operator's CRDs are
	// installed but Prometheus isn't scraping the project namespace — which
	// is the typical AIO setup. Operators who want pod metrics scraped can
	// add a PodMonitor out-of-band; we don't need to bake it into the CRD.
	//
	// customQueriesConfigMap also dropped: CNPG looks for the configMap in
	// the project namespace; we only ship it to cnpg-system + platform.
	// Reintroducing means copying the configMap on namespace creation,
	// which we can do later if anyone actually consumes those queries.
	monitoring := map[string]interface{}{
		"enablePodMonitor": false,
	}

	spec := map[string]interface{}{
		"postgresql": postgresql,
		"monitoring": monitoring,
	}
	applyTierSizing(spec, opts.Tier, opts.StorageClass)

	// The gateway sidecar is opt-in per cluster (EXC-409). A cluster that
	// names no plugin is never handed to the injector, so an ordinary
	// project's pods are untouched by any of this.
	plugins := buildDocumentDBPlugins(opts)
	if opts.Backup != nil {
		plugins = append(plugins, backupPlugin(opts.ProjectID))
	}
	if len(plugins) > 0 {
		spec["plugins"] = plugins
	}
	if opts.DocumentDB {
		spec["stopDelay"] = int64(documentDBStopDelaySeconds)
		spec["smartShutdownTimeout"] = int64(documentDBSmartShutdownSeconds)
	}

	if initdb := buildInitDB(opts); len(initdb) > 0 {
		spec["bootstrap"] = map[string]interface{}{"initdb": initdb}
	}

	if opts.ImageName != "" {
		spec["imageName"] = opts.ImageName
	}
	addServerAltDNSNames(spec, serverAltDNSNames(opts))

	return spec
}

// buildInitDB gives a DocumentDB cluster its extension and gateway role from
// CNPG's initdb job, which finishes before the first instance pod is created:
// the gateway in that pod then never starts ahead of the role it logs in as.
func buildInitDB(opts PostgreSQLClusterOpts) map[string]interface{} {
	initdb := namedAppDatabase(opts)
	if initdb == nil {
		initdb = map[string]interface{}{}
	}
	if opts.DocumentDB {
		statements := config.DocumentDBBootstrapSQL()
		postInit := make([]interface{}, len(statements))
		for i, statement := range statements {
			postInit[i] = statement
		}
		initdb["postInitSQL"] = postInit
	}
	return initdb
}

func addServerAltDNSNames(spec map[string]interface{}, names []string) {
	if len(names) == 0 {
		return
	}
	altNames := make([]interface{}, len(names))
	for i, name := range names {
		altNames[i] = name
	}
	spec["certificates"] = map[string]interface{}{"serverAltDNSNames": altNames}
}

// buildPostgresql builds the postgresql config section. Only tenant-tunable
// parameters are taken from opts.Parameters, and the platform's own settings
// are written after them so they always win.
func buildPostgresql(opts PostgreSQLClusterOpts) map[string]interface{} {
	params := map[string]interface{}{}
	for k, v := range opts.Parameters {
		if config.TenantTunableParameter(k) {
			params[k] = v
		}
	}
	params["max_connections"] = "100"
	maps.Copy(params, tierPlatformParameters(opts.Tier))
	var sharedPreloadLibs []interface{}
	// DocumentDB's configuration is applied after the tenant's, and wins.
	// Both settings are load-bearing: without the libraries the extension
	// does not load, and with pg_cron pointed elsewhere its DDL path cannot
	// run — either way the project would be recorded as DocumentDB while
	// not actually being one.
	if opts.DocumentDB {
		sharedPreloadLibs = documentDBLibraries()
		params[config.DocumentDBCronDatabaseSetting] = config.DocumentDBDatabase
		params["cron.host"] = cnpgSocketDirectory
		params[documentDBLocalhostSetting] = "host=" + cnpgSocketDirectory
	}

	postgresql := map[string]interface{}{
		"parameters": params,
		// CNPG's default pg_hba only permits the internal streaming_replica user
		// over TLS with client cert auth. We grant replication to a dedicated
		// cdc_watcher role (created post-bootstrap by createProjectRoles) and
		// regular client access to app + excalibase_app + auth_admin via
		// password auth.
		"pg_hba": networkLogins(!opts.AllowPlaintext),
	}
	if opts.DocumentDB {
		postgresql["pg_hba"] = append(documentDBLoopbackTrust(opts), postgresql["pg_hba"].([]interface{})...)
		postgresql["pg_ident"] = documentDBPeerIdentities(opts)
	}
	if len(sharedPreloadLibs) > 0 {
		postgresql["shared_preload_libraries"] = sharedPreloadLibs
	}

	return postgresql
}

// DocumentDBCredentialSecretName is the Secret in the project's own namespace
// that the gateway container's environment references. It is scoped to the
// project rather than taking the plugin's default name, which is unqualified
// and would have every tenant's gateway reading a Secret of the same name.
//
// Its values are deliberately empty: a DocumentDB project has one credential,
// its own application role, and the gateway is told to mint nothing. See
// internal/provisioner/documentdb_credential.go.
func DocumentDBCredentialSecretName(projectID string) string {
	return projectID + "-documentdb-credentials"
}

// buildDocumentDBPlugins registers the CNPG-I sidecar injector on a DocumentDB
// project's cluster, and nothing at all on any other.
//
// Every parameter is given explicitly. The plugin has a default for each, and
// each default is wrong here: its gateway image floats with the plugin's own
// version, its credential Secret name is unqualified and shared, and with no
// TLS secret the gateway generates a self-signed certificate no client can
// verify. Naming the cluster's own serving certificate instead means a Mongo
// client verifies against the same CA the endpoint API already hands out for
// Postgres.
//
// An unpinned gateway image registers no plugin: falling through to the
// plugin's default would put an image nobody chose inside a tenant's pod.
func buildDocumentDBPlugins(opts PostgreSQLClusterOpts) []interface{} {
	if !opts.DocumentDB || opts.DocumentDBGatewayImage == "" {
		return nil
	}
	return []interface{}{
		map[string]interface{}{
			"name":    config.DocumentDBPluginName,
			"enabled": true,
			"parameters": map[string]interface{}{
				"gatewayImage":               opts.DocumentDBGatewayImage,
				"documentDbCredentialSecret": DocumentDBCredentialSecretName(opts.ProjectID),
				"gatewayTLSSecret":           opts.ProjectID + postgresSuffix + "-server",
			},
		},
	}
}

// documentDBLibraries is DocumentDB's preload list, in the shape the
// cluster's shared_preload_libraries takes.
func documentDBLibraries() []interface{} {
	required := config.DocumentDBPreloadLibraries()
	libraries := make([]interface{}, 0, len(required))
	for _, library := range required {
		libraries = append(libraries, library)
	}
	return libraries
}

// BuildRestoreCluster builds the project's cluster as BuildPostgreSQLCluster
// does, bootstrapped by recovering the source project's backups through
// BuildRecoverySourceObjectStore's store instead of by initdb.
func BuildRestoreCluster(opts RestoreClusterOpts) (*unstructured.Unstructured, error) {
	if err := validateTierSizing(opts.Cluster.Tier); err != nil {
		return nil, err
	}
	if _, err := objectStoreConfiguration(opts.SourceProjectID, opts.Store); err != nil {
		return nil, err
	}
	recovery := map[string]interface{}{"source": recoverySourceName}
	for key, value := range namedAppDatabase(opts.Cluster) {
		recovery[key] = value
	}
	if opts.RecoveryTarget != nil {
		recovery["recoveryTarget"] = opts.RecoveryTarget
	}

	cluster := BuildPostgreSQLCluster(opts.Cluster)
	spec := cluster.Object["spec"].(map[string]interface{})
	spec["bootstrap"] = map[string]interface{}{"recovery": recovery}
	spec["externalClusters"] = []interface{}{recoverySource(opts.Cluster.ProjectID)}
	return cluster, nil
}

// BuildScheduledBackup builds a CNPG ScheduledBackup CRD, refusing a schedule
// CloudNativePG would misread.
func BuildScheduledBackup(projectID, namespace, schedule string) (*unstructured.Unstructured, error) {
	return scheduledBackup(projectID, namespace, schedule, false)
}

// BuildFirstScheduledBackup is a ScheduledBackup that also takes a backup as
// soon as it is created, for a cluster with no base backup of its own yet.
func BuildFirstScheduledBackup(projectID, namespace, schedule string) (*unstructured.Unstructured, error) {
	return scheduledBackup(projectID, namespace, schedule, true)
}

func scheduledBackup(projectID, namespace, schedule string, immediate bool) (*unstructured.Unstructured, error) {
	cnpgSchedule, err := CNPGBackupSchedule(schedule)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": cnpgAPIVersion,
			"kind":       "ScheduledBackup",
			"metadata": map[string]interface{}{
				"name":      projectID + "-postgres-backup",
				"namespace": namespace,
			},
			"spec": scheduledBackupSpec(projectID, cnpgSchedule, immediate),
		},
	}, nil
}

// BuildManualBackup builds a CNPG Backup CRD for on-demand backup.
func BuildManualBackup(projectID, namespace, backupName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": cnpgAPIVersion,
			"kind":       "Backup",
			"metadata": map[string]interface{}{
				"name":      backupName,
				"namespace": namespace,
			},
			"spec": pluginBackupSpec(projectID + postgresSuffix),
		},
	}
}

func scheduledBackupSpec(projectID, schedule string, immediate bool) map[string]interface{} {
	spec := pluginBackupSpec(projectID + postgresSuffix)
	spec["schedule"] = schedule
	spec["backupOwnerReference"] = "self"
	spec["immediate"] = immediate
	return spec
}
