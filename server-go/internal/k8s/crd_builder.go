package k8s

import (
	"fmt"
	"slices"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/config"
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
}

type BackupOpts struct {
	Schedule      string
	RetentionDays int
	// EndpointURL is the S3-compatible storage endpoint Barman writes to.
	// For Cloudflare R2: https://<account_id>.r2.cloudflarestorage.com
	// For AWS S3: empty (the key is omitted and Barman defaults to AWS).
	// A localstack/floci endpoint is only ever used when set explicitly.
	EndpointURL string
	// Bucket is the destination bucket name. Path within the bucket is
	// automatically scoped to the project: s3://<bucket>/<projectID>/...
	// Empty defaults to "postgres-backups" (legacy default).
	Bucket string
	// SecretName is the K8s Secret holding ACCESS_KEY_ID + ACCESS_SECRET_KEY.
	// Same field names work for AWS S3, R2, MinIO, floci/localstack — Barman
	// is provider-agnostic at this layer.
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
	SourceProjectID string
	NewProjectID    string
	Namespace       string
	Store           ObjectStoreOpts
	// RecoveryTarget is the optional PITR target (targetTime / targetXID /
	// targetLSN / targetName) in CNPG's recoveryTarget shape.
	RecoveryTarget map[string]interface{}
	// ImageName is the catalogue image for the source's major; a physical
	// recovery cannot start on any other.
	ImageName         string
	ServerAltDNSNames []string
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

// documentDBLoopbackTrust lets the gateway, which only does passwordless local
// logins, reach Postgres as itself and as the roles clients authenticate as.
// Loopback only: nothing outside the pod matches these lines.
func documentDBLoopbackTrust(opts PostgreSQLClusterOpts) []interface{} {
	roles := []string{config.DocumentDBGatewayRole, clusterOwner(opts), appRoleName}
	lines := make([]interface{}, 0, 2*len(roles))
	for _, role := range roles {
		lines = append(lines,
			"host all "+role+" 127.0.0.1/32 trust",
			"host all "+role+" ::1/128 trust")
	}
	return lines
}

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
	roles := []string{"documentdb_bg_worker_role", config.DocumentDBGatewayRole, clusterOwner(opts), appRoleName}
	lines := make([]interface{}, 0, len(roles))
	for _, role := range roles {
		lines = append(lines, "local postgres "+role)
	}
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

// buildClusterSpec builds the spec section of a CNPG Cluster CRD.
func buildClusterSpec(opts PostgreSQLClusterOpts) map[string]interface{} {
	dbName := clusterDatabaseName(opts)
	dbUser := clusterOwner(opts)

	postgresql, storage := buildPostgresqlAndStorage(opts)

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
		"instances":  int64(opts.Tier.Instances),
		"storage":    storage,
		"postgresql": postgresql,
		"monitoring": monitoring,
		"resources": map[string]interface{}{
			"requests": map[string]interface{}{
				"memory": opts.Tier.Memory,
				"cpu":    opts.Tier.CPU,
			},
			"limits": map[string]interface{}{
				"memory": opts.Tier.Memory,
				"cpu":    opts.Tier.CPU,
			},
		},
	}

	// The gateway sidecar is opt-in per cluster (EXC-409). A cluster that
	// names no plugin is never handed to the injector, so an ordinary
	// project's pods are untouched by any of this.
	if plugins := buildDocumentDBPlugins(opts); len(plugins) > 0 {
		spec["plugins"] = plugins
	}
	if opts.DocumentDB {
		spec["stopDelay"] = int64(documentDBStopDelaySeconds)
		spec["smartShutdownTimeout"] = int64(documentDBSmartShutdownSeconds)
	}

	if dbName != "app" || dbUser != "app" {
		spec["bootstrap"] = map[string]interface{}{
			"initdb": map[string]interface{}{
				"database": dbName,
				"owner":    dbUser,
			},
		}
	}

	if opts.Backup != nil {
		spec["backup"] = buildBackupSpec(opts.ProjectID, opts.Backup)
	}

	if opts.ImageName != "" {
		spec["imageName"] = opts.ImageName
	}
	addServerAltDNSNames(spec, serverAltDNSNames(opts))

	return spec
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

// buildPostgresqlAndStorage builds the postgresql config and storage sections.
func buildPostgresqlAndStorage(opts PostgreSQLClusterOpts) (map[string]interface{}, map[string]interface{}) {
	params := map[string]interface{}{
		"max_connections": "100",
	}
	// Runaway-query guard (per tier). Cancel queries and idle-in-transaction
	// sessions past the tier's budget so one tenant can't peg a shared box with
	// a never-ending query. Overridable via opts.Parameters below.
	if opts.Tier.StatementTimeout != "" {
		params["statement_timeout"] = opts.Tier.StatementTimeout
		params["idle_in_transaction_session_timeout"] = opts.Tier.StatementTimeout
	}
	var sharedPreloadLibs []interface{}
	for k, v := range opts.Parameters {
		if k == "shared_preload_libraries" {
			for _, lib := range strings.Split(v, ",") {
				sharedPreloadLibs = append(sharedPreloadLibs, strings.TrimSpace(lib))
			}
		} else {
			params[k] = v
		}
	}
	// DocumentDB's configuration is applied after the tenant's, and wins.
	// Both settings are load-bearing: without the libraries the extension
	// does not load, and with pg_cron pointed elsewhere its DDL path cannot
	// run — either way the project would be recorded as DocumentDB while
	// not actually being one.
	if opts.DocumentDB {
		sharedPreloadLibs = withDocumentDBLibraries(sharedPreloadLibs)
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
		"pg_hba": []interface{}{
			"host replication cdc_watcher all scram-sha-256",
			"host all app all scram-sha-256",
			"host all " + appRoleName + " all scram-sha-256",
			"host all auth_admin all scram-sha-256",
		},
	}
	if opts.DocumentDB {
		postgresql["pg_hba"] = append(documentDBLoopbackTrust(opts), postgresql["pg_hba"].([]interface{})...)
		postgresql["pg_ident"] = documentDBPeerIdentities(opts)
	}
	if len(sharedPreloadLibs) > 0 {
		postgresql["shared_preload_libraries"] = sharedPreloadLibs
	}

	storage := map[string]interface{}{"size": opts.Tier.StorageSize}
	if opts.StorageClass != "" {
		storage["storageClass"] = opts.StorageClass
	}

	return postgresql, storage
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

// withDocumentDBLibraries returns the cluster's preload list with DocumentDB's
// own libraries present. The tenant's entries are kept — a project may well
// want pg_stat_statements alongside DocumentDB — and an entry DocumentDB
// already requires is not repeated, because Postgres reads the list as a set
// and a duplicate only makes the parameter harder to read.
func withDocumentDBLibraries(tenant []interface{}) []interface{} {
	required := config.DocumentDBPreloadLibraries()
	libraries := make([]interface{}, 0, len(required)+len(tenant))
	for _, library := range required {
		libraries = append(libraries, library)
	}
	for _, entry := range tenant {
		name, ok := entry.(string)
		if !ok {
			continue
		}
		if !slices.Contains(required, name) {
			libraries = append(libraries, name)
		}
	}
	return libraries
}

// buildBackupSpec builds the backup section of the CNPG Cluster spec.
// Provider-agnostic: works for AWS S3, Cloudflare R2, MinIO, floci. The
// caller picks via BackupOpts{EndpointURL, Bucket, SecretName}; bucket and
// secret keep legacy defaults, the endpoint never does — an empty endpoint
// means AWS S3, never a localstack mock.
func buildBackupSpec(projectID string, backup *BackupOpts) map[string]interface{} {
	store := ObjectStoreOpts{EndpointURL: backup.EndpointURL, Bucket: backup.Bucket, SecretName: backup.SecretName}
	if store.Bucket == "" {
		store.Bucket = DefaultBackupBucket
	}
	if store.SecretName == "" {
		store.SecretName = "backup-s3-creds"
	}
	barman := buildBarmanObjectStore(projectID, store)
	barman["wal"] = map[string]interface{}{"compression": "gzip", "maxParallel": int64(2)}
	barman["data"] = map[string]interface{}{"compression": "gzip"}
	return map[string]interface{}{
		"retentionPolicy":   fmt.Sprintf("%dd", backup.RetentionDays),
		"barmanObjectStore": barman,
	}
}

const (
	// DefaultBackupBucket is the bucket Barman writes to when the backup
	// options carry none (legacy default).
	DefaultBackupBucket = "postgres-backups"
	// barmanServerName is the Barman server name under destinationPath;
	// every base backup and WAL segment lands beneath it.
	barmanServerName = "cloud"
)

// BarmanObjectPrefix is the object-key prefix (relative to the bucket) that
// holds everything Barman wrote for a project: destinationPath/serverName/.
// The deprovision purge deletes exactly this prefix, so it is derived from
// the same values buildBarmanObjectStore puts in the CRD.
func BarmanObjectPrefix(projectID string) string {
	return projectID + "/" + barmanServerName + "/"
}

// buildBarmanObjectStore is the one place the barmanObjectStore block is
// shaped, so backup (write), restore (read) and purge (delete) agree on
// serverName, destinationPath, endpoint and credential keys.
func buildBarmanObjectStore(projectID string, store ObjectStoreOpts) map[string]interface{} {
	barman := map[string]interface{}{
		"serverName":      barmanServerName,
		"destinationPath": fmt.Sprintf("s3://%s/%s", store.Bucket, projectID),
		"s3Credentials": map[string]interface{}{
			"accessKeyId":     map[string]interface{}{"name": store.SecretName, "key": "ACCESS_KEY_ID"},
			"secretAccessKey": map[string]interface{}{"name": store.SecretName, "key": "ACCESS_SECRET_KEY"},
		},
	}
	if store.EndpointURL != "" {
		barman["endpointURL"] = store.EndpointURL
	}
	return barman
}

// BuildRestoreCluster builds a CNPG Cluster CRD that bootstraps by
// recovering the source project's Barman backups from the given store.
func BuildRestoreCluster(opts RestoreClusterOpts) *unstructured.Unstructured {
	recovery := map[string]interface{}{"source": "clusterBackup"}
	if opts.RecoveryTarget != nil {
		recovery["recoveryTarget"] = opts.RecoveryTarget
	}
	barman := buildBarmanObjectStore(opts.SourceProjectID, opts.Store)
	barman["wal"] = map[string]interface{}{"maxParallel": int64(8)}
	spec := map[string]interface{}{
		"instances": int64(1),
		"storage":   map[string]interface{}{"size": "5Gi"},
		"bootstrap": map[string]interface{}{"recovery": recovery},
		"externalClusters": []interface{}{
			map[string]interface{}{"name": "clusterBackup", "barmanObjectStore": barman},
		},
	}
	if opts.ImageName != "" {
		spec["imageName"] = opts.ImageName
	}
	addServerAltDNSNames(spec, opts.ServerAltDNSNames)
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": cnpgAPIVersion,
			"kind":       "Cluster",
			"metadata": map[string]interface{}{
				"name":      opts.NewProjectID + postgresSuffix,
				"namespace": opts.Namespace,
			},
			"spec": spec,
		},
	}
}

// BuildScheduledBackup builds a CNPG ScheduledBackup CRD.
func BuildScheduledBackup(projectID, namespace, schedule string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": cnpgAPIVersion,
			"kind":       "ScheduledBackup",
			"metadata": map[string]interface{}{
				"name":      projectID + "-postgres-backup",
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"schedule":             schedule,
				"backupOwnerReference": "self",
				"cluster": map[string]interface{}{
					"name": projectID + postgresSuffix,
				},
				"immediate": false,
				"target":    "prefer-standby",
			},
		},
	}
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
			"spec": map[string]interface{}{
				"cluster": map[string]interface{}{
					"name": projectID + postgresSuffix,
				},
			},
		},
	}
}
