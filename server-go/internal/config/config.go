package config

import (
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/natsauth"
)

var errDockerCloudUnsupported = errors.New(
	"PROVISIONER_MODE=docker is single-tenant only; DEPLOYMENT_MODE=cloud (multi-tenant) is not supported on the docker provisioner — use k8s for multi-tenant")

type AppConfig struct {
	Port          string
	StoragePath   string
	LogLevel      string
	PlatformDBURL string
	NatsURL       string
	// NATS bus identity (EXC-324). NatsUser/NatsPassword are this service's
	// own credential; the callout fields let it answer the NATS server's
	// auth_callout requests for every other principal.
	NatsUser              string
	NatsPassword          string
	NatsGraphQLPassword   string
	NatsPgDogPassword     string
	NatsCDCStream         string
	NatsCalloutAccount    string
	NatsCalloutUser       string
	NatsCalloutPassword   string
	NatsCalloutIssuerSeed string
	CORSOrigins           []string
	WatcherChartPath      string
	// WatcherImage is the pinned reference every tenant watcher runs,
	// e.g. excalibase/excalibase-watcher-go:1.0.0@sha256:… (EXC-346).
	WatcherImage      string
	DenoRuntimeURL    string
	DenoRuntimeSecret string
	DenoNamespace     string
	DenoRuntimeImage  string
	VaultURL          string
	VaultPAT          string
	DeploymentMode    string // "selfhosted" (default) or "cloud"
	PublicBaseURL     string // base URL for function invoke + SDK snippets, e.g. https://api.excalibase.io
	RegistrationMode  string // "open" (default) or "invite" — invite closes open studio signup

	// ExposureEnforced is the installation-wide kill switch for the table
	// exposure filter (EXC-400). Enforcement is ON for every project and
	// there is no per-project opt-in: a project's end users reach exactly
	// the tables its grant list names, and nothing when it names none.
	//
	// This setting exists so the product decision can be reversed without
	// reverting code. It is platform-wide, never per project, and no HTTP
	// route can write it — only EXCALIBASE_EXPOSURE_ENFORCED at boot.
	ExposureEnforced bool

	// JWTRequireAud gates the end-user JWT audience check (EXC-11). On by
	// default; set JWT_REQUIRE_AUD=false only for a phased rollout where
	// older tokens without an aud claim are still in circulation.
	JWTRequireAud bool
	// JWTAudPrefix is prepended to the project id to form the audience each
	// per-project token must carry, e.g. "excalibase:proj_p1". Must match
	// the auth service's AUTH_AUD_PREFIX.
	JWTAudPrefix string

	// K8s client connection — priority: remote API > kubeconfig path > env KUBECONFIG > in-cluster > ~/.kube/config
	KubeconfigPath         string // explicit kubeconfig file
	KubeAPIURL             string // remote API server URL (for out-of-cluster platform deployments)
	KubeBearerToken        string // ServiceAccount token for remote API
	KubeCACert             string // PEM-encoded CA cert for remote API TLS
	KubeInsecureSkipVerify bool   // disable TLS verification (dev only)

	// SES + R2 are read directly from K8s secrets at startup; see main.go.
	// We only carry the public-facing knobs (URLs, default From) in config
	// so per-deployment overrides don't require touching the secret.
	EmailFromAddress string
	EmailFromName    string
	EmailProductName string
	EmailReplyTo     string
	StoragePublicURL string

	// LokiURL points at the cluster's Loki HTTP endpoint. When set, the
	// admin /logs endpoint proxies queries to Loki and the per-project
	// /logs endpoint uses Loki instead of kubectl-exec tail (which hangs
	// on busy minikube clusters and only sees logs since pod start).
	LokiURL string

	// FnEgressDefaultHosts is the operator-level outbound allowlist every
	// project's edge functions get in addition to their own setting:
	// comma-separated host, host:port or "*.suffix" entries in Deno
	// net-permission form. Empty (default) = projects start with no egress.
	// Parsed by edgefn.ParseEgressHostList; a malformed list stops the server.
	FnEgressDefaultHosts string

	// PromURL points at the cluster's Prometheus query endpoint. Used by
	// the admin handler to enrich the project list with live CPU + memory.
	// Optional — when empty, admin/projects responses omit usage fields.
	PromURL string

	// CapacityHeadroomPercent is the % of node Allocatable held back as a
	// safety buffer when computing usable capacity. Allocatable already
	// excludes kube-reserved + system-reserved (kubelet does that), so this
	// is purely OUR cushion for burst, monitoring agent growth, and brief
	// pod-restart spikes. Default 15. Set 0 to plan against full Allocatable.
	CapacityHeadroomPercent int

	// Provisioner selection — "k8s" (default) or "docker". When "docker",
	// databases are provisioned as containers on the Docker daemon instead
	// of CNPG clusters on Kubernetes.
	ProvisionerMode string
	// DockerDBPublic exposes provisioned DB container ports on 0.0.0.0 (host's
	// network interface) instead of 127.0.0.1. Default false = loopback only:
	// the DB is reachable by the app internally but not from the LAN/internet.
	// Set true only when the customer needs to connect external clients directly.
	DockerDBPublic  bool
	DockerNetwork   string // user-defined docker network for provisioned DB containers
	DockerHost      string // explicit Docker URI; empty → env → unix socket
	DockerCertPath  string // TLS certificate directory (ca.pem, cert.pem, key.pem)
	DockerTLSVerify bool

	// RestoreReadyTimeout bounds how long a restore waits for the recovered
	// database to be observed ready before it gives up and compensates.
	RestoreReadyTimeout time.Duration

	// PauseTimeout bounds each observed wait inside a pause: the pre-pause
	// backup finishing and the project's database stopping.
	PauseTimeout time.Duration

	// PlatformDBMaxConns is the platform database pool size. See the
	// arithmetic on MinPlatformDBMaxConns and in OPERATOR.md.
	PlatformDBMaxConns int

	// SchedulerEnabled runs the function scheduler: the per-project sweep
	// that drains deferred tasks and enqueues due cron jobs.
	// EXCALIBASE_SCHEDULER_ENABLED=false leaves it off (operators running
	// the worker out of process don't want a replica competing for claims).
	SchedulerEnabled bool
	// SchedulerPollInterval is the task-queue cadence
	// (EXCALIBASE_SCHEDULER_POLL_MS) and SchedulerCronInterval the cron
	// registry cadence (EXCALIBASE_CRON_POLL_MS).
	SchedulerPollInterval time.Duration
	SchedulerCronInterval time.Duration

	// Scheduler limits. Every row the sweep reads was written by a tenant,
	// so these are the platform's bounds on what a tenant's rows may cost:
	// rows claimed per tick, invocations in flight per project and per
	// replica, args size, retry budget, the finest cron cadence honoured and
	// how many cron rows one project may have walked per tick.
	SchedulerBatch              int
	SchedulerProjectConcurrency int
	SchedulerGlobalConcurrency  int
	SchedulerMaxArgsBytes       int
	SchedulerMaxAttempts        int
	CronMinInterval             time.Duration
	CronMaxJobsPerProject       int
	// SchedulerProjectTimeout bounds one project's whole sweep. A tenant owns
	// its database and can make any statement there hang; this is what keeps
	// that cost to the one tenant instead of the whole scheduler.
	SchedulerProjectTimeout time.Duration
	// SchedulerClaimLease is how long a claimed task may stay 'running'
	// before the sweep treats the claim as abandoned and puts the row back.
	// Must outlast one invocation.
	SchedulerClaimLease time.Duration

	// Project database pooling. Each cached tenant pool costs connections on
	// that tenant's database and file descriptors here, so both the pool size
	// and the number of cached pools are bounded.
	ProjectDBMaxOpenConns int
	ProjectDBMaxPools     int
	// ProjectDBStatementTimeout and ProjectDBLockTimeout are the server-side
	// bounds set on every tenant connection the platform opens. Abandoning a
	// statement client-side leaves it running on the tenant's server; these
	// are the bounds the tenant cannot outlast.
	ProjectDBStatementTimeout time.Duration
	ProjectDBLockTimeout      time.Duration

	// AutoMigrate applies a bundle's declared schema to the project database
	// at deploy time. EXCALIBASE_AUTO_MIGRATE=false defers it to an explicit
	// POST /api/projects/{projectId}/schema/apply.
	AutoMigrate bool

	// Provider selection. A provider named here must arrive with the
	// settings it needs; the boot-time wiring check refuses a deployment
	// that names one and leaves it half-configured.
	//
	// EmailProvider is what the operator named: "ses", "resend" or "noop".
	// Empty means unset — the sender falls back to SES if its credentials
	// happen to be there, and to a no-op sender otherwise. Naming a
	// provider is what makes its settings required at boot.
	EmailProvider       string
	SESAccessKeyID      string
	SESSecretAccessKey  string
	SESRegion           string
	SESConfigurationSet string
	ResendAPIKey        string

	// Object storage (R2/S3-compatible). Empty across the board means the
	// storage feature is off; a partial set is a misconfiguration.
	R2AccessKeyID     string
	R2SecretAccessKey string
	R2Endpoint        string
	R2Bucket          string
	R2Region          string

	// Backup destination defaults. They fall back to the object-storage
	// credentials, which is how a single-bucket deployment is configured.
	BackupAccessKeyID     string
	BackupSecretAccessKey string
	BackupEndpoint        string
	BackupBucket          string
	BackupRegion          string

	// FnReplayEnabled re-deploys a project's functions when its runtime
	// reports a new boot id; FnReplayPollInterval is how often that is
	// checked.
	FnReplayEnabled      bool
	FnReplayPollInterval time.Duration
	// Public database endpoints (EXC-410). DBEndpointDomain is the suffix a
	// customer's endpoint name hangs off — they dial
	// <projectId>.<DBEndpointDomain>:<port> — and empty means the platform
	// offers no public endpoints at all, so the API refuses to enable one
	// rather than handing out a name nothing resolves. DBEndpointPorts is
	// the window ports are allocated randomly from,
	// DBEndpointPortQuarantine how long a freed one is held back before it
	// can be reissued, and DBEndpointSharedIPKey the MetalLB sharing key
	// that puts every project's Service on the one public address.
	DBEndpointDomain         string
	DBEndpointPorts          domain.PortRange
	DBEndpointPortQuarantine time.Duration
	DBEndpointSharedIPKey    string

	// AutoPauseEnabled runs the hourly idle-pause sweep (EXC-280): projects on
	// tiers with autoPauseAfterDays > 0 are warned at N-1 idle days and paused
	// at N. EXCALIBASE_AUTOPAUSE_ENABLED overrides; defaults on in cloud mode,
	// off self-hosted (a single operator owns their own projects).
	AutoPauseEnabled bool

	// AppHostingEnabled mounts the customer-application routes (EXC-377).
	// App hosting is built incrementally on main and must stay unreachable in
	// any install until the epic is finished, so it defaults off; only the
	// exact value "true" turns it on.
	AppHostingEnabled bool
}

// IsCloud returns true when running in cloud deployment mode. Derived from
// DeploymentMode so the rest of the code has a single boolean to branch on.
// Cloud mode:
//   - Uses Postgres for platform store + vault backend (PLATFORM_DB_URL required)
//   - Enables multi-org create/delete + tier enforcement endpoints
//   - Enforces tier limits in the provisioning service
//
// Self-hosted mode (default):
//   - Uses Postgres for platform store + vault backend, auto-init/unseal at boot
//   - Single default org, no tier enforcement, no billing endpoints
func (c AppConfig) IsCloud() bool {
	return c.DeploymentMode == "cloud"
}

// Validate rejects unsupported config combinations at boot (fail-fast).
// Docker mode is single-tenant by design (one customer/team/org, prod or dev);
// cloud mode is the multi-tenant control plane. Running them together would put
// a multi-tenant control plane on a runtime with no cross-tenant isolation, so
// it is refused rather than silently insecure.
func (c AppConfig) Validate() error {
	if c.ProvisionerMode == "docker" && c.IsCloud() {
		return errDockerCloudUnsupported
	}
	return nil
}

func Load() AppConfig {
	deploymentMode := envOr("DEPLOYMENT_MODE", "selfhosted")
	return AppConfig{
		AutoPauseEnabled:         envBool("EXCALIBASE_AUTOPAUSE_ENABLED", deploymentMode == "cloud"),
		Port:                     envOr("PORT", "24005"),
		StoragePath:              envOr("STORAGE_PATH", "../provisioning-data"),
		LogLevel:                 envOr("LOG_LEVEL", "debug"),
		PlatformDBURL:            envOr("PLATFORM_DB_URL", ""),
		NatsURL:                  envOr("NATS_URL", ""),
		NatsUser:                 envOr("NATS_USER", natsauth.PrincipalProvisioning),
		NatsPassword:             os.Getenv("NATS_PASSWORD"),
		NatsGraphQLPassword:      os.Getenv("NATS_GRAPHQL_PASSWORD"),
		NatsPgDogPassword:        os.Getenv("NATS_PGDOG_PASSWORD"),
		NatsCDCStream:            envOr("NATS_CDC_STREAM", "CDC"),
		NatsCalloutAccount:       envOr("NATS_AUTH_CALLOUT_ACCOUNT", "APP"),
		NatsCalloutUser:          envOr("NATS_AUTH_CALLOUT_USER", "auth-callout"),
		NatsCalloutPassword:      os.Getenv("NATS_AUTH_CALLOUT_PASSWORD"),
		NatsCalloutIssuerSeed:    os.Getenv("NATS_AUTH_CALLOUT_ISSUER_SEED"),
		RegistrationMode:         envOr("REGISTRATION_MODE", "open"),
		ExposureEnforced:         exposureEnforced(),
		DBEndpointDomain:         envDBEndpointDomain("EXCALIBASE_DB_ENDPOINT_DOMAIN"),
		DBEndpointPorts:          envDBEndpointPortRange("EXCALIBASE_DB_ENDPOINT_PORT_RANGE"),
		DBEndpointPortQuarantine: envDuration("EXCALIBASE_DB_ENDPOINT_PORT_QUARANTINE", DefaultDBEndpointPortQuarantine),
		DBEndpointSharedIPKey:    envOr("EXCALIBASE_DB_ENDPOINT_SHARED_IP_KEY", DefaultDBEndpointSharedIPKey),

		JWTRequireAud:               envBool("JWT_REQUIRE_AUD", true),
		JWTAudPrefix:                envOr("AUTH_AUD_PREFIX", "excalibase:"),
		CORSOrigins:                 parseCORSOrigins(envOr("CORS_ORIGINS", "https://app.excalibase.io")),
		WatcherChartPath:            envOr("WATCHER_CHART_PATH", "/charts/excalibase-watcher"),
		WatcherImage:                os.Getenv("WATCHER_IMAGE"),
		DenoRuntimeURL:              envOr("DENO_RUNTIME_URL", "http://deno-runtime.serverless.svc.cluster.local:8000"),
		DenoRuntimeSecret:           envOr("DENO_RUNTIME_SECRET", ""),
		DenoNamespace:               envOr("DENO_NAMESPACE", "serverless"),
		DenoRuntimeImage:            envOr("DENO_RUNTIME_IMAGE", "excalibase/deno-runtime:latest"),
		VaultURL:                    envOr("VAULT_URL", ""),
		VaultPAT:                    envOr("VAULT_PAT", ""),
		DeploymentMode:              deploymentMode,
		PublicBaseURL:               envOr("PUBLIC_BASE_URL", "https://api.excalibase.io"),
		KubeconfigPath:              envOr("KUBECONFIG_PATH", ""),
		KubeAPIURL:                  envOr("KUBE_API_URL", ""),
		KubeBearerToken:             envOr("KUBE_BEARER_TOKEN", ""),
		KubeCACert:                  envOr("KUBE_CA_CERT", ""),
		KubeInsecureSkipVerify:      envOr("KUBE_INSECURE_SKIP_VERIFY", "") == "true",
		EmailFromAddress:            envOr("EMAIL_FROM", "noreply@excalibase.io"),
		EmailFromName:               envOr("EMAIL_FROM_NAME", "Excalibase"),
		EmailProductName:            envOr("EMAIL_PRODUCT_NAME", "Excalibase"),
		EmailReplyTo:                envOr("EMAIL_REPLY_TO", ""),
		StoragePublicURL:            envOr("STORAGE_PUBLIC_URL", ""),
		LokiURL:                     envOr("LOKI_URL", ""),
		PromURL:                     envOr("PROM_URL", ""),
		FnEgressDefaultHosts:        envOr("EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS", ""),
		CapacityHeadroomPercent:     envInt("CAPACITY_HEADROOM_PERCENT", 15),
		ProvisionerMode:             envOr("PROVISIONER_MODE", "k8s"),
		DockerDBPublic:              envOr("DOCKER_DB_PUBLIC", "") == "true",
		DockerNetwork:               envOr("DOCKER_NETWORK", ""),
		DockerHost:                  envOr("DOCKER_HOST", ""),
		DockerCertPath:              envOr("DOCKER_CERT_PATH", ""),
		DockerTLSVerify:             envOr("DOCKER_TLS_VERIFY", "") != "",
		RestoreReadyTimeout:         envDuration("EXCALIBASE_RESTORE_READY_TIMEOUT", defaultRestoreReadyTimeout),
		PauseTimeout:                envDuration("EXCALIBASE_PAUSE_TIMEOUT", defaultPauseTimeout),
		PlatformDBMaxConns:          envPlatformDBMaxConns(),
		SchedulerEnabled:            envBool("EXCALIBASE_SCHEDULER_ENABLED", true),
		SchedulerPollInterval:       envMillis("EXCALIBASE_SCHEDULER_POLL_MS", defaultSchedulerPoll),
		SchedulerCronInterval:       envMillis("EXCALIBASE_CRON_POLL_MS", defaultSchedulerCronPoll),
		AutoMigrate:                 envBool("EXCALIBASE_AUTO_MIGRATE", true),
		SchedulerBatch:              envPositiveInt("EXCALIBASE_SCHEDULER_BATCH", 32),
		SchedulerProjectConcurrency: envPositiveInt("EXCALIBASE_SCHEDULER_PROJECT_CONCURRENCY", 4),
		SchedulerGlobalConcurrency:  envPositiveInt("EXCALIBASE_SCHEDULER_CONCURRENCY", 32),
		SchedulerMaxArgsBytes:       envPositiveInt("EXCALIBASE_SCHEDULER_MAX_ARGS_BYTES", 1024*1024),
		SchedulerMaxAttempts:        envPositiveInt("EXCALIBASE_SCHEDULER_MAX_ATTEMPTS", 5),
		CronMinInterval:             envMillis("EXCALIBASE_CRON_MIN_INTERVAL_MS", time.Minute),
		CronMaxJobsPerProject:       envPositiveInt("EXCALIBASE_CRON_MAX_JOBS", 100),
		SchedulerProjectTimeout:     envMillis("EXCALIBASE_SCHEDULER_PROJECT_TIMEOUT_MS", defaultSchedulerProjectTimeout),
		SchedulerClaimLease:         envMillis("EXCALIBASE_SCHEDULER_CLAIM_LEASE_MS", defaultSchedulerClaimLease),
		ProjectDBMaxOpenConns:       envPositiveInt("EXCALIBASE_PROJECT_DB_MAX_CONNS", 2),
		ProjectDBMaxPools:           envPositiveInt("EXCALIBASE_PROJECT_DB_MAX_POOLS", 64),
		ProjectDBStatementTimeout:   envMillis("EXCALIBASE_PROJECT_DB_STATEMENT_TIMEOUT_MS", defaultProjectDBStatementTimeout),
		ProjectDBLockTimeout:        envMillis("EXCALIBASE_PROJECT_DB_LOCK_TIMEOUT_MS", defaultProjectDBLockTimeout),
		EmailProvider:               strings.ToLower(strings.TrimSpace(os.Getenv("EMAIL_PROVIDER"))),
		SESAccessKeyID:              os.Getenv("SES_ACCESS_KEY_ID"),
		SESSecretAccessKey:          os.Getenv("SES_SECRET_ACCESS_KEY"),
		SESRegion:                   envOr("SES_REGION", "us-east-1"),
		SESConfigurationSet:         os.Getenv("SES_CONFIGURATION_SET"),
		ResendAPIKey:                os.Getenv("RESEND_API_KEY"),
		R2AccessKeyID:               os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretAccessKey:           os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2Endpoint:                  os.Getenv("R2_ENDPOINT"),
		R2Bucket:                    os.Getenv("R2_BUCKET"),
		R2Region:                    os.Getenv("R2_REGION"),
		BackupAccessKeyID:           envOr("BACKUP_DEFAULT_ACCESS_KEY_ID", os.Getenv("R2_ACCESS_KEY_ID")),
		BackupSecretAccessKey:       envOr("BACKUP_DEFAULT_SECRET_ACCESS_KEY", os.Getenv("R2_SECRET_ACCESS_KEY")),
		BackupEndpoint:              envOr("BACKUP_DEFAULT_ENDPOINT", os.Getenv("R2_ENDPOINT")),
		BackupBucket:                envOr("BACKUP_DEFAULT_BUCKET", "excalibase-backups"),
		BackupRegion:                envOr("BACKUP_DEFAULT_REGION", "auto"),
		FnReplayEnabled:             envBool("EXCALIBASE_FN_REPLAY_ENABLED", true),
		FnReplayPollInterval:        envMillis("EXCALIBASE_FN_REPLAY_POLL_MS", defaultReplayPoll),
		AppHostingEnabled:           envOr("APP_HOSTING_ENABLED", "") == "true",
	}
}

// defaultReplayPoll matches edgefn.DefaultReplayInterval; kept here so the
// operator-facing default lives with the rest of the configuration.
const defaultReplayPoll = 15 * time.Second

// Scheduler cadences. 5s is a compromise between task latency and the load
// a sweep puts on every tenant database; 60s is the finest granularity a
// 5-field cron expression can express anyway.
const (
	defaultSchedulerPoll     = 5 * time.Second
	defaultSchedulerCronPoll = 60 * time.Second
)

// Bounds on a tenant database that will not answer. 30s is long enough for a
// batch of claims on a healthy project and short enough that a hostile one
// costs the sweep a single tick; a lock not granted in 5s is one being held.
const (
	defaultSchedulerProjectTimeout   = 30 * time.Second
	defaultProjectDBStatementTimeout = 30 * time.Second
	defaultProjectDBLockTimeout      = 5 * time.Second
	// Comfortably past the 40s invocation timeout, so a lease only expires
	// on a replica that died or a runtime that never answered.
	defaultSchedulerClaimLease = 5 * time.Minute
)

// parseMillis reads an interval given in milliseconds. An empty value means
// "not set"; anything present but unusable is an error rather than a
// silently substituted default.
func parseMillis(raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	ms, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%q is not a whole number of milliseconds", raw)
	}
	if ms <= 0 {
		return 0, fmt.Errorf("%q must be positive", raw)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

func envMillis(key string, fallback time.Duration) time.Duration {
	d, err := parseMillis(os.Getenv(key), fallback)
	if err != nil {
		log.Fatalf("%s: %v", key, err)
	}
	return d
}

// Flag is one operator switch: the environment variable that sets it, the
// AppConfig field it lands on, and its effective value. The boot-time
// wiring check walks these so an enabled feature with a missing dependency
// cannot start.
type Flag struct {
	Env     string
	Field   string
	Enabled bool
}

// FeatureFlags declares every boolean switch on AppConfig. Adding a boolean
// to AppConfig without adding it here fails the config package's own test,
// which is what keeps the wiring check exhaustive.
func (c AppConfig) FeatureFlags() []Flag {
	return []Flag{
		{Env: "EXCALIBASE_SCHEDULER_ENABLED", Field: "SchedulerEnabled", Enabled: c.SchedulerEnabled},
		{Env: "EXCALIBASE_AUTO_MIGRATE", Field: "AutoMigrate", Enabled: c.AutoMigrate},
		{Env: "EXCALIBASE_AUTOPAUSE_ENABLED", Field: "AutoPauseEnabled", Enabled: c.AutoPauseEnabled},
		{Env: "EXCALIBASE_FN_REPLAY_ENABLED", Field: "FnReplayEnabled", Enabled: c.FnReplayEnabled},
		{Env: "JWT_REQUIRE_AUD", Field: "JWTRequireAud", Enabled: c.JWTRequireAud},
		{Env: "KUBE_INSECURE_SKIP_VERIFY", Field: "KubeInsecureSkipVerify", Enabled: c.KubeInsecureSkipVerify},
		{Env: "DOCKER_DB_PUBLIC", Field: "DockerDBPublic", Enabled: c.DockerDBPublic},
		{Env: "DOCKER_TLS_VERIFY", Field: "DockerTLSVerify", Enabled: c.DockerTLSVerify},
		{Env: exposureEnvKey, Field: "ExposureEnforced", Enabled: c.ExposureEnforced},
		{Env: "APP_HOSTING_ENABLED", Field: "AppHostingEnabled", Enabled: c.AppHostingEnabled},
	}
}

func parseCORSOrigins(raw string) []string {
	if raw == "" {
		log.Fatal("CORS_ORIGINS must be set; refusing to start with no origin allowlist")
	}
	if raw == "*" {
		return []string{"*"}
	}
	parts := strings.Split(raw, ",")
	origins := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			origins = append(origins, p)
		}
	}
	if len(origins) == 0 {
		log.Fatal("CORS_ORIGINS contained only empty values; refusing to start")
	}
	return origins
}

// defaultRestoreReadyTimeout bounds the wait for a recovered database to be
// observed ready. Restores replay WAL, so the budget is generous.
const defaultRestoreReadyTimeout = 15 * time.Minute

// defaultPauseTimeout bounds each observed wait inside a pause. Stopping a
// busy database takes minutes: the pre-pause backup has to finish and
// postgres has to shut down cleanly.
const defaultPauseTimeout = 10 * time.Minute

// DefaultPlatformDBMaxConns is the platform database pool size. It leaves
// room for roughly a dozen concurrent lifecycle operations alongside ordinary
// query traffic.
const DefaultPlatformDBMaxConns = 20

// MinPlatformDBMaxConns is the smallest pool that can do any work at all:
// five standing leadership claims (backup scheduler, idle pause, storage
// reap, restore sweep, and the function scheduler's cron half) pin one
// connection each, plus one for a lifecycle operation in flight and one for
// a query. Tenant databases are reached through their own pools and cost
// nothing here.
const MinPlatformDBMaxConns = 7

// parsePlatformDBMaxConns reads the pool size, treating an empty value as
// "not set". A value that is present but unusable is an error rather than a
// silent fallback: an operator who set it deliberately must not be left with
// a pool they did not choose and no way to notice.
func parsePlatformDBMaxConns(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return DefaultPlatformDBMaxConns, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", raw)
	}
	if n < MinPlatformDBMaxConns {
		return 0, fmt.Errorf(
			"%d is below the minimum of %d: the control plane holds 5 standing leadership claims, plus one connection per lifecycle operation in flight and one for a query",
			n, MinPlatformDBMaxConns)
	}
	return n, nil
}

// envPlatformDBMaxConns refuses to start on a pool that cannot work.
func envPlatformDBMaxConns() int {
	n, err := parsePlatformDBMaxConns(os.Getenv("PLATFORM_DB_MAX_CONNS"))
	if err != nil {
		log.Fatalf("PLATFORM_DB_MAX_CONNS: %v", err)
	}
	return n
}

// parseDuration reads a Go duration, treating an empty value as "not set".
// A value that is present but unreadable is an error: silently falling back
// would run the platform on a budget the operator did not choose.
func parseDuration(raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%q is not a duration (e.g. 15m, 90s)", raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%q must be positive", raw)
	}
	return d, nil
}

// envDuration refuses to start on a misconfigured duration rather than
// silently substituting the default.
func envDuration(key string, fallback time.Duration) time.Duration {
	d, err := parseDuration(os.Getenv(key), fallback)
	if err != nil {
		log.Fatalf("%s: %v", key, err)
	}
	return d
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envBool reads a boolean flag. Accepts 1/true/yes/on and 0/false/no/off
// (case-insensitive); unset or unrecognised values yield the fallback.
func envBool(key string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return fallback
}

// parsePositiveInt reads a whole number that must be positive. Present but
// unusable is an error: a limit the operator did not choose is not a limit.
func parsePositiveInt(raw string, fallback int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%q is not a whole number", raw)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%q must be positive", raw)
	}
	return n, nil
}

func envPositiveInt(key string, fallback int) int {
	n, err := parsePositiveInt(os.Getenv(key), fallback)
	if err != nil {
		log.Fatalf("%s: %v", key, err)
	}
	return n
}

// exposureEnvKey names the one platform-wide exposure kill switch (EXC-400).
const exposureEnvKey = "EXCALIBASE_EXPOSURE_ENFORCED"

// exposureEnforced reads the kill switch. Unset means ON. A value that is not
// a boolean stops the server rather than being guessed at: an operator who
// mistypes it must not discover the state of their exposure filter in
// production.
func exposureEnforced() bool {
	raw := os.Getenv(exposureEnvKey)
	if strings.TrimSpace(raw) == "" {
		return true
	}
	enforced, err := parseStrictBool(raw)
	if err != nil {
		log.Fatalf("%s: %v", exposureEnvKey, err)
	}
	if !enforced {
		log.Printf("!!! EXPOSURE ENFORCEMENT IS OFF for this entire installation (%s=%s): "+
			"every project's tables and functions are served to end users unfiltered, "+
			"whatever its grant list says. Unset %s to restore enforcement.",
			exposureEnvKey, raw, exposureEnvKey)
	}
	return enforced
}

// parseStrictBool accepts the same spellings as envBool but refuses anything
// else, including the empty string, instead of substituting a default.
func parseStrictBool(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a boolean (use true/false, 1/0, yes/no, on/off)", raw)
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
