package config

import (
	"errors"
	"log"
	"os"
	"strconv"
	"strings"

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
	DenoRuntimeURL        string
	DenoRuntimeSecret     string
	DenoNamespace         string
	DenoRuntimeImage      string
	VaultURL              string
	VaultPAT              string
	DeploymentMode        string // "selfhosted" (default) or "cloud"
	PublicBaseURL         string // base URL for function invoke + SDK snippets, e.g. https://api.excalibase.io
	RegistrationMode      string // "open" (default) or "invite" — invite closes open studio signup

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

	// AutoPauseEnabled runs the hourly idle-pause sweep (EXC-280): projects on
	// tiers with autoPauseAfterDays > 0 are warned at N-1 idle days and paused
	// at N. EXCALIBASE_AUTOPAUSE_ENABLED overrides; defaults on in cloud mode,
	// off self-hosted (a single operator owns their own projects).
	AutoPauseEnabled bool
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
		AutoPauseEnabled:        envBool("EXCALIBASE_AUTOPAUSE_ENABLED", deploymentMode == "cloud"),
		Port:                    envOr("PORT", "24005"),
		StoragePath:             envOr("STORAGE_PATH", "../provisioning-data"),
		LogLevel:                envOr("LOG_LEVEL", "debug"),
		PlatformDBURL:           envOr("PLATFORM_DB_URL", ""),
		NatsURL:                 envOr("NATS_URL", ""),
		NatsUser:                envOr("NATS_USER", natsauth.PrincipalProvisioning),
		NatsPassword:            os.Getenv("NATS_PASSWORD"),
		NatsGraphQLPassword:     os.Getenv("NATS_GRAPHQL_PASSWORD"),
		NatsPgDogPassword:       os.Getenv("NATS_PGDOG_PASSWORD"),
		NatsCDCStream:           envOr("NATS_CDC_STREAM", "CDC"),
		NatsCalloutAccount:      envOr("NATS_AUTH_CALLOUT_ACCOUNT", "APP"),
		NatsCalloutUser:         envOr("NATS_AUTH_CALLOUT_USER", "auth-callout"),
		NatsCalloutPassword:     os.Getenv("NATS_AUTH_CALLOUT_PASSWORD"),
		NatsCalloutIssuerSeed:   os.Getenv("NATS_AUTH_CALLOUT_ISSUER_SEED"),
		RegistrationMode:        envOr("REGISTRATION_MODE", "open"),
		JWTRequireAud:           envBool("JWT_REQUIRE_AUD", true),
		JWTAudPrefix:            envOr("AUTH_AUD_PREFIX", "excalibase:"),
		CORSOrigins:             parseCORSOrigins(envOr("CORS_ORIGINS", "https://app.excalibase.io")),
		WatcherChartPath:        envOr("WATCHER_CHART_PATH", "/charts/excalibase-watcher"),
		DenoRuntimeURL:          envOr("DENO_RUNTIME_URL", "http://deno-runtime.serverless.svc.cluster.local:8000"),
		DenoRuntimeSecret:       envOr("DENO_RUNTIME_SECRET", ""),
		DenoNamespace:           envOr("DENO_NAMESPACE", "serverless"),
		DenoRuntimeImage:        envOr("DENO_RUNTIME_IMAGE", "excalibase/deno-runtime:latest"),
		VaultURL:                envOr("VAULT_URL", ""),
		VaultPAT:                envOr("VAULT_PAT", ""),
		DeploymentMode:          deploymentMode,
		PublicBaseURL:           envOr("PUBLIC_BASE_URL", "https://api.excalibase.io"),
		KubeconfigPath:          envOr("KUBECONFIG_PATH", ""),
		KubeAPIURL:              envOr("KUBE_API_URL", ""),
		KubeBearerToken:         envOr("KUBE_BEARER_TOKEN", ""),
		KubeCACert:              envOr("KUBE_CA_CERT", ""),
		KubeInsecureSkipVerify:  envOr("KUBE_INSECURE_SKIP_VERIFY", "") == "true",
		EmailFromAddress:        envOr("EMAIL_FROM", "noreply@excalibase.io"),
		EmailFromName:           envOr("EMAIL_FROM_NAME", "Excalibase"),
		EmailProductName:        envOr("EMAIL_PRODUCT_NAME", "Excalibase"),
		EmailReplyTo:            envOr("EMAIL_REPLY_TO", ""),
		StoragePublicURL:        envOr("STORAGE_PUBLIC_URL", ""),
		LokiURL:                 envOr("LOKI_URL", ""),
		PromURL:                 envOr("PROM_URL", ""),
		FnEgressDefaultHosts:    envOr("EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS", ""),
		CapacityHeadroomPercent: envInt("CAPACITY_HEADROOM_PERCENT", 15),
		ProvisionerMode:         envOr("PROVISIONER_MODE", "k8s"),
		DockerDBPublic:          envOr("DOCKER_DB_PUBLIC", "") == "true",
		DockerNetwork:           envOr("DOCKER_NETWORK", ""),
		DockerHost:              envOr("DOCKER_HOST", ""),
		DockerCertPath:          envOr("DOCKER_CERT_PATH", ""),
		DockerTLSVerify:         envOr("DOCKER_TLS_VERIFY", "") != "",
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

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}
