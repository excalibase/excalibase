package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/bootstrap"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/excalibase/provisioning-poc/pkg/kmsseal"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	if handleCLIArgs() {
		return
	}
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}
	// KMS-wrapped unseal: when VAULT_UNSEAL_KEY_CIPHERTEXT is set, decrypt it into
	// VAULT_UNSEAL_KEY before the embedded vault's env-based auto-unseal runs, so no
	// plaintext unseal key is stored anywhere. No-op when unset (legacy plaintext).
	if err := kmsseal.ResolveUnsealKeyEnv(context.Background()); err != nil {
		log.Fatalf("KMS unseal-key resolve: %v", err)
	}
	runServer(cfg)
}

// handleCLIArgs processes CLI subcommands. Returns true if a subcommand was handled.
func handleCLIArgs() bool {
	if len(os.Args) <= 1 {
		return false
	}
	switch os.Args[1] {
	case "reset-password":
		resetPasswordCLI()
		return true
	case "recover-instances":
		recoverInstancesCLI()
		return true
	case "kms-encrypt-unseal":
		kmsEncryptUnsealCLI()
		return true
	case "help", "--help", "-h":
		printHelp()
		return true
	}
	return false
}

// kmsEncryptUnsealCLI wraps a plaintext unseal key under a KMS key and prints the
// base64 ciphertext to store in platform-bootstrap/unseal-key-ciphertext. One-time
// operator step for KMS-envelope auto-unseal (kmsUnseal.enabled). Reads KMS_KEY_ID
// + VAULT_UNSEAL_KEY (or args), honours AWS_ENDPOINT_URL_KMS (floci in CI).
func kmsEncryptUnsealCLI() {
	keyID := os.Getenv("KMS_KEY_ID")
	if len(os.Args) > 2 && os.Args[2] != "" {
		keyID = os.Args[2]
	}
	unseal := os.Getenv("VAULT_UNSEAL_KEY")
	if len(os.Args) > 3 && os.Args[3] != "" {
		unseal = os.Args[3]
	}
	if keyID == "" || unseal == "" {
		fmt.Fprintln(os.Stderr, "usage: kms-encrypt-unseal <kms-key-id> <unseal-key>  (or KMS_KEY_ID + VAULT_UNSEAL_KEY env)")
		os.Exit(2)
	}
	c, err := kmsseal.NewClient(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "kms client: %v\n", err)
		os.Exit(1)
	}
	ct, err := kmsseal.EncryptUnsealKey(context.Background(), c, keyID, unseal)
	if err != nil {
		fmt.Fprintf(os.Stderr, "encrypt: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(ct)
}

// printHelp prints CLI usage information.
func printHelp() {
	fmt.Println("Usage: excalibase-server [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  (none)              Start the HTTP server")
	fmt.Println("  reset-password      Reset admin password (vault-gated)")
	fmt.Println("  recover-instances   Re-discover K8s clusters into SQLite (vault-gated)")
	fmt.Println("  kms-encrypt-unseal  Wrap the unseal key under KMS → base64 ciphertext")
	fmt.Println("  help                Show this help")
}

// runServer initialises all dependencies and starts the HTTP server.
func runServer(cfg config.AppConfig) {
	sqlStore := buildPlatformStore(cfg)
	defer sqlStore.Close()

	// Use platform store as InstanceStore (same interface)
	var store storage.InstanceStore = sqlStore

	// Keep filesystem store as fallback for parameter groups (until migrated)
	pgStore, _ := storage.NewFileSystemParameterGroupStore(cfg.StoragePath)

	vc, localVault, vaultCleanup := buildVault(cfg, sqlStore)
	defer vaultCleanup()

	bootstrapDefaultOrgIfNeeded(cfg, sqlStore)

	k8sClient := buildK8sClient(cfg)
	factory, dockerClientRef := buildProvisionerFactory(cfg, k8sClient)

	fnHandler := buildFunctionHandler(cfg, vc, store, sqlStore, k8sClient)

	provSvc, provCleanup := buildProvisioningService(cfg, store, sqlStore, factory, k8sClient, vc, dockerClientRef)
	defer provCleanup()

	deps := buildHandlerDeps(handlerDepsArgs{
		cfg:          cfg,
		store:        store,
		sqlStore:     sqlStore,
		k8sClient:    k8sClient,
		vc:           vc,
		localVault:   localVault,
		provSvc:      provSvc,
		pgStore:      pgStore,
		dockerClient: dockerClientRef,
	})
	deps.fnHandler = fnHandler

	policyPub, err := service.NewPolicyChangePublisher(cfg.NatsURL)
	if err != nil {
		log.Printf("WARN: policy change publisher: %v", err)
	} else {
		defer policyPub.Close()
		deps.rlsPolicyHandler.SetPublisher(policyPub)
	}

	scheduler, schedulerStop := startBackupScheduler(cfg, sqlStore, deps.backupHandler)
	defer schedulerStop()
	if scheduler != nil {
		deps.backupHandler.SetScheduler(scheduler)
	}

	// Phase 8.5: deferred-execution scheduler — drains the Postgres-backed
	// task queue + walks the cron registry. Enabled by default; disable
	// via EXCALIBASE_SCHEDULER_ENABLED=false (operators running the worker
	// out-of-process don't want the in-process replica to compete on the
	// FOR UPDATE SKIP LOCKED claim path).
	fnSchedHandles := startFunctionScheduler(sqlStore)
	defer fnSchedHandles.Stop()

	// Wire pause/resume — backup must run before pause, so PauseService
	// depends on the BackupService that backupHandler exposes.
	pausers := buildPausers(cfg, factory, dockerClientRef)
	if len(pausers) > 0 {
		pauseSvc := service.NewPauseService(service.PauseServiceConfig{
			Instances: store,
			Pausers:   pausers,
			Backups:   deps.backupHandler.Service(),
		})
		deps.provHandler.SetPauseService(pauseSvc)
		deps.provHandler.SetInstanceStore(store)
	}

	if sqlStore != nil {
		wireRestoreOrchestrator(sqlStore, store, deps)
	}

	r := buildRouter(cfg, sqlStore, store, deps)

	startServer(cfg, r)
}

// wireRestoreOrchestrator builds the restore orchestrator, registers the
// single delegate-to-adapter step, sweeps stale jobs, and wires it into the
// backup handler. Extracted from runServer to keep that function's branching
// shallow.
func wireRestoreOrchestrator(sqlStore storage.PlatformStore, store storage.InstanceStore, deps *handlerDeps) {
	orchestrator := service.NewRestoreOrchestrator(service.RestoreOrchestratorConfig{
		Jobs: sqlStore.RestoreJobs(),
	})
	backupSvc := deps.backupHandler.Service()
	// Wrap the existing synchronous adapter.Restore as a single
	// orchestrator step. K8s mode keeps its CNPG flow untouched
	// (BackupService dispatches to K8sBackupAdapter); Docker mode
	// returns the same RESTORING placeholder Phase 1B ships with.
	// When the WAL-G fetch / restore-runner lands we replace this
	// step with a multi-stage pipeline — RestoreOrchestrator's
	// step list is the only thing that needs to change.
	orchestrator.SetSteps([]service.RestoreStep{
		{
			Name: "delegate-to-adapter",
			Run: func(ctx context.Context, j *domain.RestoreJob) error {
				return runRestoreStep(ctx, store, backupSvc, j)
			},
		},
	})
	_ = orchestrator.SweepStale(context.Background())
	deps.backupHandler.SetRestoreOrchestrator(orchestrator)
}

// runRestoreStep resolves the source instance and dispatches the restore to
// the backup service, translating the job's target kind into a RestoreRequest.
func runRestoreStep(ctx context.Context, store storage.InstanceStore, backupSvc *service.BackupService, j *domain.RestoreJob) error {
	inst, err := store.FindByProjectID(j.SourceProjectID)
	if err != nil || inst == nil {
		return fmt.Errorf("source project %s not found", j.SourceProjectID)
	}
	req := domain.RestoreRequest{NewProjectID: j.NewProjectID}
	switch j.TargetKind {
	case "time":
		if t, err := time.Parse(time.RFC3339, j.TargetValue); err == nil {
			req.TargetTime = &domain.FlexTime{Time: t}
		}
	case "xid":
		req.TargetXID = j.TargetValue
	case "lsn":
		req.TargetLSN = j.TargetValue
	case "name":
		req.TargetName = j.TargetValue
	}
	_, err = backupSvc.RestoreFromBackup(ctx, j.SourceProjectID, req)
	return err
}

// startBackupScheduler launches the cron runtime and replays the
// persistent schedule. Returns the scheduler (so the handler can be
// wired post-construction) and a stop function for graceful shutdown.
// In single-process self-hosted deployments the leader lock is a
// no-op; cloud uses a Postgres advisory lock so multi-replica
// platforms only fire once per tick.
func startBackupScheduler(cfg config.AppConfig, sqlStore storage.PlatformStore, backupHandler *handler.BackupHandler) (*service.BackupScheduler, func()) {
	if backupHandler == nil || sqlStore == nil {
		return nil, func() {
			// no-op stop: scheduler was never started, nothing to release.
		}
	}
	var lock service.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() {
		// FNV-1a("excalibase-backup-scheduler") — distinct from any
		// other advisory lock the platform might use.
		lock = pgstore.NewAdvisoryLock(sqlStore.DB(), 0x6168_0acb_4233_4b21)
	}
	scheduler := service.NewBackupScheduler(service.BackupSchedulerConfig{
		Schedules: sqlStore.BackupSchedules(),
		Backups:   backupHandler.Service(),
		Lock:      lock,
	})
	if err := scheduler.Start(context.Background()); err != nil {
		log.Printf("WARN: backup scheduler start: %v", err)
		return nil, func() {
			// no-op stop: scheduler failed to start, nothing to release.
		}
	}
	return scheduler, scheduler.Stop
}

// startFunctionScheduler boots Phase 8.5's deferred-execution worker +
// cron runner. The runners poll the platform DB; tenants are responsible
// for ensuring excalibase_scheduled_functions + excalibase_cron_jobs
// exist on whichever DB the runners point at. Boot is best-effort —
// when the platform DB isn't a real *sql.DB (e.g. SQLite self-hosted),
// we skip the boot rather than panicking.
func startFunctionScheduler(sqlStore storage.PlatformStore) *bootstrap.SchedulerHandles {
	cfg := bootstrap.SchedulerConfigFromEnv()
	if !cfg.Enabled {
		log.Println("Function scheduler disabled via EXCALIBASE_SCHEDULER_ENABLED")
		return bootstrap.StartScheduler(context.Background(), cfg)
	}
	if sqlStore == nil {
		return bootstrap.StartScheduler(context.Background(), cfg)
	}
	cfg.DB = sqlStore.DB()
	if cfg.DB == nil {
		log.Println("Function scheduler boot skipped: platform store has no *sql.DB handle")
		// Disable to take the no-op path inside StartScheduler.
		cfg.Enabled = false
		return bootstrap.StartScheduler(context.Background(), cfg)
	}
	handles := bootstrap.StartScheduler(context.Background(), cfg)
	if handles.Started() {
		log.Printf("Function scheduler started (poll=%v, cronPoll=%v)",
			cfg.PollInterval, cfg.CronPollInterval)
	}
	return handles
}

// handlerDeps groups all wired handlers + middleware used during route mounting.
// Centralising the bag keeps buildRouter focused on routing rather than wiring.
type handlerDeps struct {
	provHandler        *handler.ProvisioningHandler
	metricsHandler     *handler.MetricsHandler
	backupHandler      *handler.BackupHandler
	perfHandler        *handler.PerformanceHandler
	auditHandler       *handler.AuditHandler
	snapshotHandler    *handler.SnapshotHandler
	migrationHandler   *handler.MigrationHandler
	alertHandler       *handler.AlertHandler
	setupHandler       *handler.SetupHandler
	pgHandler          *handler.ParameterGroupHandler
	emailTokensHandler *handler.EmailTokensHandler
	storageHandler     *handler.StorageHandler
	adminHandler       *handler.AdminHandler
	authHandler        *handler.AuthHandler
	orgHandler         *handler.OrgHandler
	vaultHandler       *handler.VaultHandler
	schemaHandler      *handler.SchemaHandler
	realtimeHandler    *handler.RealtimeHandler
	fnHandler          *handler.FunctionHandler
	rlsPolicyHandler   *handler.RlsPolicyHandler
	tierHandler        *handler.TierHandler
	capDeps            *capacityDeps
	rlUnauth           func(http.Handler) http.Handler
	rlAuthed           func(http.Handler) http.Handler
	rlDataPlane        func(http.Handler) http.Handler
}

// buildFunctionStore picks where tenant function source lives. Cloud mode uses
// the platform Postgres store: STORAGE_PATH is an emptyDir in the AIO chart, so
// the filesystem layout loses every tenant's code on pod restart, reschedule, or
// scale-to-zero (EXC-333). Self-hosted/dev keeps the filesystem store.
func buildFunctionStore(cfg config.AppConfig, sqlStore storage.PlatformStore) edgefn.Store {
	if cfg.IsCloud() {
		if pg, ok := sqlStore.(*pgstore.Store); ok {
			log.Println("Cloud mode: edge functions stored in platform Postgres")
			return edgefn.NewPostgresFunctionStore(pg.DB())
		}
		log.Println("WARN: cloud mode without a Postgres platform store — " +
			"edge functions fall back to STORAGE_PATH and will NOT survive a pod restart")
	}
	return edgefn.NewFunctionStore(cfg.StoragePath)
}

// buildFunctionHandler wires the edge-function handler with stores + runtime client.
func buildFunctionHandler(cfg config.AppConfig, vc vaultclient.VaultClient, store storage.InstanceStore, sqlStore storage.PlatformStore, k8sClient k8s.KubeClient) *handler.FunctionHandler {
	fnStore := buildFunctionStore(cfg, sqlStore)
	fnSecrets := edgefn.NewSecretsStore(vc)
	fnClient := edgefn.NewRuntimeClient(cfg.DenoRuntimeURL, cfg.DenoRuntimeSecret)
	fnHandler := handler.NewFunctionHandler(fnStore, fnSecrets, fnClient, store, sqlStore, cfg.PublicBaseURL)
	if cfg.ProvisionerMode != "docker" {
		fnHandler.SetK8sClient(k8sClient, cfg.DenoRuntimeImage, cfg.DenoRuntimeSecret)
	}
	fnHandler.SetVault(vc)
	return fnHandler
}

// buildProvisioningService wires the central provisioning service plus its
// optional collaborators (backup defaults, PgDog notifier, etc). Returns the
// service and a cleanup function for any goroutine-owning collaborators.
func buildProvisioningService(
	cfg config.AppConfig,
	store storage.InstanceStore,
	sqlStore storage.PlatformStore,
	factory *provisioner.Factory,
	k8sClient k8s.KubeClient,
	vc vaultclient.VaultClient,
	dockerClientRef provisioner.DockerClient,
) (*service.ProvisioningService, func()) {
	provSvc := service.NewProvisioningService(store, factory, k8sClient)
	provSvc.SetVault(vc)
	provSvc.SetOrgStore(sqlStore)
	provSvc.SetTierStore(sqlStore)
	provSvc.SetSelfHostedMode(!cfg.IsCloud())
	provSvc.SetCapacityHeadroom(cfg.CapacityHeadroomPercent)
	if cfg.ProvisionerMode == "docker" {
		provSvc.SetDefaultDeploymentMode(domain.ModeDocker)
	} else {
		provSvc.SetDefaultDeploymentMode(domain.ModeK8s)
	}

	provSvc.SetBackupDefaults(&service.BackupDefaults{
		AccessKeyID:     envOr("BACKUP_DEFAULT_ACCESS_KEY_ID", os.Getenv("R2_ACCESS_KEY_ID")),
		SecretAccessKey: envOr("BACKUP_DEFAULT_SECRET_ACCESS_KEY", os.Getenv("R2_SECRET_ACCESS_KEY")),
		Endpoint:        envOr("BACKUP_DEFAULT_ENDPOINT", os.Getenv("R2_ENDPOINT")),
		Bucket:          envOr("BACKUP_DEFAULT_BUCKET", "excalibase-backups"),
		Region:          envOr("BACKUP_DEFAULT_REGION", "auto"),
	})
	if name := os.Getenv("REALTIME_PUBLICATION_NAME"); name != "" {
		provSvc.SetPublicationName(name)
	}
	if dockerClientRef != nil {
		provSvc.SetDockerClient(dockerClientRef)
	}
	provSvc.SetLokiURL(cfg.LokiURL)

	cleanup := wirePgDogNotifier(cfg, sqlStore, provSvc)
	return provSvc, cleanup
}

// wirePgDogNotifier returns a cleanup func that closes the notifier on shutdown,
// or a no-op when prerequisites (Postgres store + NATS) are unmet.
func wirePgDogNotifier(cfg config.AppConfig, sqlStore storage.PlatformStore, provSvc *service.ProvisioningService) func() {
	noop := func() {
		// no-op cleanup: notifier was never started, nothing to close.
	}
	if cfg.PlatformDBURL == "" || cfg.NatsURL == "" {
		return noop
	}
	pgStore, ok := sqlStore.(storage.PgDogConfigStore)
	if !ok {
		return noop
	}
	pgdogNotifier, err := service.NewPgDogNotifier(pgStore, cfg.NatsURL)
	if err != nil {
		log.Printf("WARN: pgdog notifier: %v", err)
		return noop
	}
	provSvc.SetPgDogNotifier(pgdogNotifier)
	log.Println("PgDog notifier enabled (NATS + platform-db)")
	return func() { pgdogNotifier.Close() }
}

type handlerDepsArgs struct {
	cfg          config.AppConfig
	store        storage.InstanceStore
	sqlStore     storage.PlatformStore
	k8sClient    k8s.KubeClient
	vc           vaultclient.VaultClient
	localVault   *vault.Vault
	provSvc      *service.ProvisioningService
	pgStore      storage.ParameterGroupStore
	dockerClient provisioner.DockerClient // optional, for Docker-mode backup adapter
}

// buildBackupService wires the BackupService with the right adapter
// map for the current deployment mode. K8s adapter is always present
// (BYOC flows through K8s today; future BYOC adapter can layer in).
// Docker adapter is added when both ProvisionerMode=docker and the
// platform has R2/S3 credentials available — without a bucket the
// Docker adapter has nowhere to put bytes, so we keep it out and the
// dispatch returns ErrUnsupportedBackupMode for docker projects until
// the operator finishes wiring credentials.
func buildBackupService(
	cfg config.AppConfig,
	store storage.InstanceStore,
	sqlStore storage.PlatformStore,
	k8sClient k8s.KubeClient,
	dockerClient provisioner.DockerClient,
) *service.BackupService {
	adapters := map[domain.DeploymentMode]service.BackupAdapter{
		domain.ModeK8s: service.NewK8sBackupAdapter(k8sClient, cfg.StoragePath),
	}

	if cfg.ProvisionerMode == "docker" && dockerClient != nil {
		dockerSDK, err := provisioner.NewRealDockerClient(provisioner.DockerClientOptions{
			Host:        os.Getenv("DOCKER_HOST"),
			CertPath:    os.Getenv("DOCKER_CERT_PATH"),
			TLSVerify:   os.Getenv("DOCKER_TLS_VERIFY") != "",
			BindAddress: dbBindAddr(cfg),
			Network:     cfg.DockerNetwork,
		})
		if err == nil {
			runner := service.NewDockerBackupRunner(dockerSDK.RawClient())
			bucket := envOr("BACKUP_DEFAULT_BUCKET", "excalibase-backups")
			endpoint := envOr("BACKUP_DEFAULT_ENDPOINT", os.Getenv("R2_ENDPOINT"))
			ak := envOr("BACKUP_DEFAULT_ACCESS_KEY_ID", os.Getenv("R2_ACCESS_KEY_ID"))
			sk := envOr("BACKUP_DEFAULT_SECRET_ACCESS_KEY", os.Getenv("R2_SECRET_ACCESS_KEY"))
			region := envOr("BACKUP_DEFAULT_REGION", "auto")
			if ak != "" && sk != "" && endpoint != "" {
				// Default path-style ON — works for R2, MinIO, LocalStack.
				// Operators targeting real AWS S3 set BACKUP_S3_PATH_STYLE=0
				// to flip to virtual-host addressing.
				usePathStyle := os.Getenv("BACKUP_S3_PATH_STYLE") != "0"
				uploader, err := service.NewAWSS3Uploader(context.Background(), service.AWSS3UploaderConfig{
					AccessKeyID:     ak,
					SecretAccessKey: sk,
					Endpoint:        endpoint,
					Region:          region,
					UsePathStyle:    usePathStyle,
				})
				if err == nil {
					dockerAdapter := service.NewDockerBackupAdapter(service.DockerBackupAdapterConfig{
						Runner:    runner,
						Uploader:  uploader,
						Records:   sqlStore.BackupRecords(),
						Bucket:    bucket,
						KeyPrefix: "backups/",
						Instances: store,
					})
					// Wire the docker client so Restore can create +
					// populate the new container. dockerClient is the
					// abstracted interface used by DockerProvisioner;
					// DockerBackupAdapter calls CreateContainer +
					// CopyToContainer + StartContainer + WaitForHealthy.
					dockerAdapter.SetDockerClient(dockerClient)
					adapters[domain.ModeDocker] = dockerAdapter
				}
			}
		}
	}

	return service.NewBackupServiceWithAdapters(store, adapters, cfg.StoragePath)
}

// buildHandlerDeps constructs every HTTP handler the router needs.
// newOrgHandler builds the org handler and wires the instance store so the
// project-member endpoints can verify a project belongs to the URL's org
// before operating on it (prevents cross-org project-member enumeration).
func newOrgHandler(sqlStore storage.PlatformStore, instances storage.InstanceStore) *handler.OrgHandler {
	h := handler.NewOrgHandler(sqlStore, sqlStore)
	h.SetInstanceStore(instances)
	return h
}

func buildHandlerDeps(a handlerDepsArgs) *handlerDeps {
	cfg, store, sqlStore := a.cfg, a.store, a.sqlStore
	k8sClient, vc, localVault := a.k8sClient, a.vc, a.localVault
	provSvc, pgStore := a.provSvc, a.pgStore

	metricsSvc := service.NewMetricsService(store, k8sClient, cfg.StoragePath)
	backupSvc := buildBackupService(a.cfg, store, sqlStore, k8sClient, a.dockerClient)
	perfSvc := service.NewPerformanceService(store, k8sClient)
	auditSvc := service.NewAuditService(store, k8sClient)
	snapshotSvc := service.NewSnapshotService(store, k8sClient, cfg.StoragePath)
	migrationSvc := service.NewMigrationService(store, k8sClient, cfg.StoragePath)
	alertSvc := service.NewAlertingService(cfg.StoragePath)
	setupSvc := service.NewOperatorSetupService(k8sClient)

	emailSender := buildEmailSender(cfg)
	storageSvc := buildStorageService(cfg, sqlStore)
	var storageHandler *handler.StorageHandler
	if storageSvc != nil {
		storageHandler = handler.NewStorageHandler(storageSvc, store)
		// Phase 10: ctx.storage internal routes share the Deno runtime
		// secret. Empty value disables the routes (all calls 401).
		storageHandler.SetRuntimeSecret(cfg.DenoRuntimeSecret)
	}
	emailTokensHandler := handler.NewEmailTokensHandler(
		sqlStore.DB(),
		emailSender,
		sqlStore,
		cfg.PublicBaseURL,
		cfg.EmailProductName,
	)

	var promClient *handler.PromClient
	if cfg.PromURL != "" {
		promClient = handler.NewPromClient(cfg.PromURL)
	}
	adminHandler := handler.NewAdminHandler(provSvc, store, sqlStore, sqlStore, k8sClient, cfg.LokiURL, promClient)
	tierHandler := handler.NewTierHandler(sqlStore)

	authHandler := handler.NewAuthHandler(sqlStore, sqlStore)
	authHandler.SetOrgStore(sqlStore)
	authHandler.SetInviteOnly(cfg.RegistrationMode == "invite")

	var vaultHandler *handler.VaultHandler
	if localVault != nil {
		vaultHandler = handler.NewVaultHandler(localVault)
	}

	realtimeHandler := handler.NewRealtimeHandler(sqlStore, sqlStore, vc)
	if name := os.Getenv("REALTIME_PUBLICATION_NAME"); name != "" {
		realtimeHandler.SetPublicationName(name)
	}

	return &handlerDeps{
		provHandler:        handler.NewProvisioningHandler(provSvc, sqlStore),
		metricsHandler:     handler.NewMetricsHandler(metricsSvc),
		backupHandler:      handler.NewBackupHandler(backupSvc),
		perfHandler:        handler.NewPerformanceHandler(perfSvc),
		auditHandler:       handler.NewAuditHandler(auditSvc),
		snapshotHandler:    handler.NewSnapshotHandler(snapshotSvc),
		migrationHandler:   handler.NewMigrationHandler(migrationSvc),
		alertHandler:       handler.NewAlertHandler(alertSvc),
		setupHandler:       handler.NewSetupHandler(setupSvc),
		pgHandler:          handler.NewParameterGroupHandler(pgStore),
		emailTokensHandler: emailTokensHandler,
		storageHandler:     storageHandler,
		adminHandler:       adminHandler,
		authHandler:        authHandler,
		orgHandler:         newOrgHandler(sqlStore, store),
		vaultHandler:       vaultHandler,
		schemaHandler:      handler.NewSchemaHandler(vc),
		realtimeHandler:    realtimeHandler,
		rlsPolicyHandler:   handler.NewRlsPolicyHandler(sqlStore.RlsPolicies()),
		tierHandler:        tierHandler,
		capDeps: &capacityDeps{
			k8sClient:       k8sClient,
			store:           store,
			headroomPercent: cfg.CapacityHeadroomPercent,
		},
		rlUnauth:    custommw.RateLimit(custommw.PerIP, 30, time.Minute),
		rlAuthed:    custommw.RateLimit(custommw.PerUser, 600, time.Minute),
		rlDataPlane: custommw.RateLimit(custommw.PerProjectAndUser, 120, time.Second),
	}
}

// buildRouter wires the chi router with global middleware and mounts every
// API subtree. The per-subtree mounting is delegated to focused helpers so
// this top-level remains a manifest of which features are exposed.
func buildRouter(cfg config.AppConfig, sqlStore storage.PlatformStore, store storage.InstanceStore, d *handlerDeps) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(custommw.SecurityHeaders)
	r.Use(custommw.CORS(cfg.CORSOrigins))
	r.Use(auth.ExtractAuth(sqlStore))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	r.Get("/api/config", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"deploymentMode":"%s"}`, cfg.DeploymentMode)
	})
	r.Get("/api/capacity", auth.RequireAuth(http.HandlerFunc(d.capDeps.serveCapacity)).ServeHTTP)

	mountProvisioningRoutes(r, sqlStore, store, d)
	mountSimpleAuthRoutes(r, d)
	mountAuthRoutes(r, d)
	mountOrgAndAdminRoutes(r, cfg, d)
	mountVaultAndSchemaRoutes(r, sqlStore, store, d)
	mountProjectScopedRoutes(r, sqlStore, store, d)
	mountEmailRoutes(r, d)

	// Phase 7: /http/* dispatch is mounted BEFORE the bare /{fnId} route so
	// chi's router doesn't treat the literal segment "http" as a function id.
	r.With(custommw.TenantContext).HandleFunc("/functions/v1/{projectId}/http/*", d.fnHandler.PublicHttpInvoke)
	r.With(custommw.TenantContext).HandleFunc("/functions/v1/{projectId}/{fnId}", d.fnHandler.PublicInvoke)
	// Internal runtime → provisioning callback for export metadata capture.
	// Authenticates via X-Excalibase-Runtime-Token (shared runtime secret),
	// not JWT — this is server-to-server only.
	r.Post("/internal/runtime/functions/{fnId}/metadata", d.fnHandler.ReceiveExportMetadata)
	// Phase 7: server-to-server bridge used by ctx.runQuery/runMutation/
	// runAction to invoke a sibling function. Same shared-secret auth as
	// the metadata callback above.
	r.Post("/internal/invoke/{projectId}/{fnId}", d.fnHandler.InternalInvoke)
	return r
}

// mountProvisioningRoutes attaches /api/provision and its per-project sub-router.
func mountProvisioningRoutes(r *chi.Mux, sqlStore storage.PlatformStore, store storage.InstanceStore, d *handlerDeps) {
	r.Route("/api/provision", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Get("/", d.provHandler.ListInstances)
		r.Post("/", d.provHandler.Provision)
		r.Post("/estimate", d.provHandler.EstimateCost)
		r.Post("/byoc", d.provHandler.ProvisionBYOC)

		r.Route("/{projectId}", func(r chi.Router) {
			r.Use(custommw.TenantContext)
			r.Use(custommw.RequireProjectAccess(store, sqlStore))
			r.Use(d.rlDataPlane)
			r.Get("/", d.provHandler.GetStatus)
			r.Delete("/", d.provHandler.Delete)
			r.Get("/credentials", d.provHandler.GetCredentials)
			r.Patch("/deletion-protection", d.provHandler.SetDeletionProtection)
			r.Get("/logs", d.provHandler.GetLogs)
			r.Post("/credentials/rotate", d.provHandler.RotateCredentials)
			r.Put("/maintenance-window", d.provHandler.SetMaintenanceWindow)
			r.Get("/maintenance-window", d.provHandler.GetMaintenanceWindow)

			r.Route("/metrics", func(r chi.Router) { d.metricsHandler.Routes(r) })
			r.Route("/backup", func(r chi.Router) { d.backupHandler.Routes(r) })
			r.Route("/performance", func(r chi.Router) { d.perfHandler.Routes(r) })
			r.Route("/audit", func(r chi.Router) { d.auditHandler.Routes(r) })
			r.Route("/snapshot", func(r chi.Router) { d.snapshotHandler.Routes(r) })
			r.Route("/migrations", func(r chi.Router) { d.migrationHandler.Routes(r) })
			r.Route("/rls-policies", func(r chi.Router) { d.rlsPolicyHandler.RlsRoutes(r) })
			r.Route("/column-policies", func(r chi.Router) { d.rlsPolicyHandler.ColumnRoutes(r) })
		})
	})
}

// mountSimpleAuthRoutes mounts the small auth-gated subtrees (alerts, setup, parameter groups).
func mountSimpleAuthRoutes(r *chi.Mux, d *handlerDeps) {
	// Read-only tier specs for any authenticated user (the provision page tier
	// selector). Editing stays admin-only under /api/admin/tiers.
	r.Route("/api/tiers", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Get("/", d.tierHandler.List)
	})
	r.Route("/api/alerts", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		d.alertHandler.Routes(r)
	})
	r.Route("/api/setup", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		d.setupHandler.Routes(r)
	})
	r.Route("/api/parameter-groups", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		d.pgHandler.Routes(r)
	})
}

// mountAuthRoutes mounts /api/auth (mixed public + authed).
func mountAuthRoutes(r *chi.Mux, d *handlerDeps) {
	r.Route("/api/auth", func(r chi.Router) {
		r.With(d.rlUnauth).Post("/register", d.authHandler.Register)
		r.With(d.rlUnauth).Post("/login", d.authHandler.Login)
		r.With(d.rlUnauth).Get("/setup-status", d.authHandler.GetSetupStatus)
		r.With(d.rlAuthed).Post("/logout", d.authHandler.Logout)
		r.With(auth.RequireAuth, d.rlAuthed).Get("/me", d.authHandler.Me)
		r.With(auth.RequireAuth, d.rlAuthed).Route("/users", func(r chi.Router) {
			r.With(auth.RequirePermission(auth.PermManageUsers)).Get("/", d.authHandler.ListUsers)
			r.With(auth.RequirePermission(auth.PermManageUsers)).Post("/", d.authHandler.CreateUser)
			r.With(auth.RequirePermission(auth.PermManageUsers)).Delete("/{userId}", d.authHandler.DeleteUser)
		})
		r.With(auth.RequireAuth, d.rlAuthed).Route("/tokens", func(r chi.Router) {
			r.Get("/", d.authHandler.ListTokens)
			r.Post("/", d.authHandler.CreateToken)
			r.Delete("/{tokenHash}", d.authHandler.RevokeToken)
		})
	})
}

// mountOrgAndAdminRoutes mounts /api/orgs and /api/admin.
func mountOrgAndAdminRoutes(r *chi.Mux, cfg config.AppConfig, d *handlerDeps) {
	r.Route("/api/orgs", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		d.orgHandler.Routes(r, cfg.IsCloud())
	})
	r.Route("/api/admin", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Group(func(r chi.Router) { d.adminHandler.Routes(r) })
		r.Route("/tiers", func(r chi.Router) {
			r.Use(auth.RequirePermission(auth.PermViewAny))
			d.tierHandler.Routes(r)
		})
	})
}

// mountVaultAndSchemaRoutes mounts the optional vault routes and /api/schema.
// The schema surface is project-scoped: {projectId} is bound at the mount so
// RequireProjectAccess runs with it in scope (EXC-349). Binding the guard one
// level higher — before {projectId} exists — would make it a silent no-op and
// let any authenticated studio user read/modify any tenant's database.
func mountVaultAndSchemaRoutes(r *chi.Mux, sqlStore storage.PlatformStore, store storage.InstanceStore, d *handlerDeps) {
	if d.vaultHandler != nil {
		r.Route("/api/vault", func(r chi.Router) { d.vaultHandler.Routes(r) })
	}
	r.Route("/api/schema/{projectId}", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Use(custommw.TenantContext)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		d.schemaHandler.RoutesInner(r)
	})
}

// mountProjectScopedRoutes mounts every /api/projects/{projectId}/* subtree.
func mountProjectScopedRoutes(r *chi.Mux, sqlStore storage.PlatformStore, store storage.InstanceStore, d *handlerDeps) {
	r.Route("/api/projects/{projectId}/functions", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		r.With(auth.RequirePermission(auth.PermViewAny)).Get("/", d.fnHandler.List)
		r.With(auth.RequirePermission(auth.PermManageFunctions)).Post("/", d.fnHandler.Create)
		r.With(auth.RequirePermission(auth.PermViewAny)).Get("/_metadata", d.fnHandler.ListExportMetadata)
		r.With(auth.RequirePermission(auth.PermViewAny)).Get("/runtime/status", d.fnHandler.RuntimeStatus)
		r.With(auth.RequirePermission(auth.PermViewAny)).Get("/secrets", d.fnHandler.ListSecrets)
		r.With(auth.RequirePermission(auth.PermManageFunctions)).Post("/secrets", d.fnHandler.SetSecret)
		r.With(auth.RequirePermission(auth.PermManageFunctions)).Delete("/secrets/{key}", d.fnHandler.DeleteSecret)
		r.Route("/{fnId}", func(r chi.Router) {
			r.With(auth.RequirePermission(auth.PermViewAny)).Get("/", d.fnHandler.Get)
			r.With(auth.RequirePermission(auth.PermManageFunctions)).Delete("/", d.fnHandler.Delete)
			r.With(auth.RequirePermission(auth.PermManageFunctions)).Post("/invoke", d.fnHandler.Invoke)
			r.With(auth.RequirePermission(auth.PermViewAny)).Get("/logs", d.fnHandler.Logs)
		})
	})
	r.Route("/api/projects/{projectId}/schema", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		r.With(auth.RequirePermission(auth.PermManageFunctions)).Post("/apply", d.fnHandler.ApplySchemaFromStore)
	})
	r.Route("/api/projects/{projectId}/info", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		r.Get("/", d.provHandler.GetProjectInfo)
	})
	r.Route("/api/projects/{projectId}/realtime", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		d.realtimeHandler.Routes(r)
	})
	if d.storageHandler != nil {
		r.Route("/api/projects/{projectId}/storage", func(r chi.Router) {
			r.Use(custommw.TenantContext)
			r.Use(auth.RequireAuth)
			r.Use(custommw.RequireProjectAccess(store, sqlStore))
			d.storageHandler.Routes(r)
		})
		d.storageHandler.PublicRoutes(r)
		// Phase 10: ctx.storage runtime-to-provisioning routes. Auth via
		// X-Excalibase-Runtime-Token shared secret — same secret the
		// Deno runtime uses for /deploy and /internal/invoke. Mounted
		// on the root router because internal callers don't have a
		// user JWT and don't ride the project-access middleware.
		d.storageHandler.InternalRoutes(r)
	}
}

// mountEmailRoutes mounts /api/email (verify + reset flows).
func mountEmailRoutes(r *chi.Mux, d *handlerDeps) {
	r.Route("/api/email", func(r chi.Router) {
		d.emailTokensHandler.Routes(r)
	})
}

// startServer binds the address and runs ListenAndServe; exits the process on error.
func startServer(cfg config.AppConfig, r *chi.Mux) {
	addr := fmt.Sprintf(":%s", cfg.Port)
	log.Printf("Excalibase Go server starting on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// buildPlatformStore opens the PostgreSQL platform store. The platform is
// Postgres-only (self-hosted runs the same CNPG platform-db as cloud), so
// PLATFORM_DB_URL is always required.
func buildPlatformStore(cfg config.AppConfig) storage.PlatformStore {
	if cfg.PlatformDBURL == "" {
		log.Fatal("PLATFORM_DB_URL (PostgreSQL connection string) is required")
	}
	pgStore, err := pgstore.New(cfg.PlatformDBURL)
	if err != nil {
		log.Fatalf("Failed to init Postgres platform store: %v", err)
	}
	log.Println("Using PostgreSQL platform store")
	return pgStore
}

// buildVault selects the vault backend (remote HTTP, Postgres-backed Shamir,
// or bbolt) and returns the client, the local instance (nil for HTTP), and a
// cleanup function that closes the local vault if any.
func buildVault(cfg config.AppConfig, sqlStore storage.PlatformStore) (vaultclient.VaultClient, *vault.Vault, func()) {
	switch {
	case cfg.VaultURL != "":
		log.Printf("Using remote vault at %s", cfg.VaultURL)
		return vaultclient.NewHTTPClient(cfg.VaultURL, cfg.VaultPAT), nil, func() {
			// no-op cleanup: remote HTTP vault has no local handle to close.
		}
	case cfg.IsCloud():
		pgStoreTyped, ok := sqlStore.(*pgstore.Store)
		if !ok {
			log.Fatal("cloud mode requires a Postgres platform store for vault backend")
		}
		vaultStore := vault.NewPostgresStore(pgStoreTyped.DB())
		localVault, vErr := vault.NewWithStore(vaultStore)
		if vErr != nil {
			log.Fatalf("Failed to init vault (postgres): %v", vErr)
		}
		log.Println("Cloud mode: using PostgreSQL vault store")
		return localVault, localVault, func() { localVault.Close() }
	default:
		vaultPath := cfg.StoragePath + "/vault.bolt"
		localVault, vErr := vault.New(vaultPath)
		if vErr != nil {
			log.Fatalf("Failed to init vault (bbolt): %v", vErr)
		}
		// Selfhosted has no bootstrap Job (that's k8s/cloud). Auto-init on first
		// run and auto-unseal on restart so the platform is usable with zero
		// operator steps; VAULT_UNSEAL_KEY, if set, is honoured instead of the
		// on-disk key.
		if err := vault.EnsureReady(localVault, cfg.StoragePath+"/unseal.key", os.Getenv("VAULT_UNSEAL_KEY")); err != nil {
			log.Fatalf("Failed to ready vault (bbolt): %v", err)
		}
		log.Println("Self-hosted mode: using bbolt vault store (auto-init/unseal)")
		return localVault, localVault, func() { localVault.Close() }
	}
}

// bootstrapDefaultOrgIfNeeded creates the default org for self-hosted mode
// once at least one user exists. Cloud mode skips this — orgs are minted via
// the org API.
func bootstrapDefaultOrgIfNeeded(cfg config.AppConfig, sqlStore storage.PlatformStore) {
	if cfg.IsCloud() {
		return
	}
	users, _ := sqlStore.FindAllUsers(context.Background())
	if len(users) == 0 {
		return
	}
	if err := auth.BootstrapDefaultOrg(context.Background(), sqlStore, users[0].ID); err != nil {
		log.Fatalf("Failed to bootstrap default org: %v", err)
	}
}

// buildK8sClient wires the K8s client honouring the resolution order
// documented in k8s.NewClientWith.
func buildK8sClient(cfg config.AppConfig) k8s.KubeClient {
	k8sOpts := k8s.ClientOptions{
		KubeconfigPath:        cfg.KubeconfigPath,
		APIURL:                cfg.KubeAPIURL,
		BearerToken:           cfg.KubeBearerToken,
		InsecureSkipTLSVerify: cfg.KubeInsecureSkipVerify,
	}
	if cfg.KubeCACert != "" {
		k8sOpts.CACertPEM = []byte(cfg.KubeCACert)
	}
	k8sClient, err := k8s.NewClientWith(k8sOpts)
	if err != nil {
		// Docker provisioner needs no Kubernetes. Degrade gracefully so the
		// platform boots; k8s-only features (CNPG backup, k8s metrics) are
		// simply unavailable in docker mode.
		if cfg.ProvisionerMode == "docker" {
			log.Printf("WARN: no K8s client (docker mode) — k8s-only features disabled: %v", err)
			return nil
		}
		log.Fatalf("Failed to init K8s client: %v", err)
	}
	return k8sClient
}

// buildProvisionerFactory selects the K8s (CNPG) or Docker provisioner based
// on PROVISIONER_MODE and returns the factory plus the docker client (nil
// when not in docker mode).
// dbBindAddr picks the host IP for published DB container ports: 0.0.0.0 when
// the operator opts into public exposure, otherwise 127.0.0.1 (internal only).
func dbBindAddr(cfg config.AppConfig) string {
	if cfg.DockerDBPublic {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

func buildProvisionerFactory(cfg config.AppConfig, k8sClient k8s.KubeClient) (*provisioner.Factory, provisioner.DockerClient) {
	if cfg.ProvisionerMode == "docker" {
		dockerClient, err := provisioner.NewRealDockerClient(provisioner.DockerClientOptions{
			Host:        cfg.DockerHost,
			CertPath:    cfg.DockerCertPath,
			TLSVerify:   cfg.DockerTLSVerify,
			BindAddress: dbBindAddr(cfg),
			Network:     cfg.DockerNetwork,
		})
		if err != nil {
			log.Fatalf("docker provisioner: %v", err)
		}
		log.Printf("Provisioner mode: docker (host=%s)", cfg.DockerHost)
		return provisioner.NewFactory(provisioner.NewDockerPostgreSQLProvisioner(dockerClient)), dockerClient
	}
	log.Println("Provisioner mode: k8s (CNPG)")
	pgProvisioner := provisioner.NewPostgreSQLProvisioner(k8sClient, cfg.WatcherChartPath)
	return provisioner.NewFactory(pgProvisioner), nil
}

// buildPausers extracts the Pauser-implementing provisioners from
// the factory + docker client. Returns an empty map when no pauser
// is wired (BYOC-only deployments). PauseService consults this map
// at request time to pick the right Pauser per instance's mode.
func buildPausers(cfg config.AppConfig, factory *provisioner.Factory, dc provisioner.DockerClient) map[domain.DeploymentMode]provisioner.Pauser {
	out := map[domain.DeploymentMode]provisioner.Pauser{}
	// Iterate the factory's registered provisioners and cherry-pick
	// those that satisfy the optional Pauser interface.
	for _, p := range factory.Registered() {
		if pauser, ok := p.(provisioner.Pauser); ok {
			// Both K8s and Docker postgres provisioners now implement
			// Pauser. The mode to register under depends on which
			// provisioner shipped the implementation, which mirrors
			// cfg.ProvisionerMode for the active path.
			if cfg.ProvisionerMode == "docker" {
				out[domain.ModeDocker] = pauser
			} else {
				out[domain.ModeK8s] = pauser
			}
		}
	}
	_ = dc // dockerClient already wrapped inside DockerPostgreSQLProvisioner
	return out
}

// envOr returns os.Getenv(key) or fallback when empty. Local helper to keep
// main.go's per-feature config blocks readable.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// buildEmailSender picks the email provider from EMAIL_PROVIDER env.
// Supported values: "ses" (default — back-compat), "resend", "noop".
// Falls back to noop on unrecognised values or missing creds so the
// platform still boots and email-dependent handlers return 503.
//
// SES creds: SES_ACCESS_KEY_ID + SES_SECRET_ACCESS_KEY (k8s secret ses-creds)
// Resend creds: RESEND_API_KEY (k8s secret resend-creds, or env directly)
// Common: EMAIL_FROM_ADDRESS / EMAIL_FROM_NAME (or legacy SES_FROM_*)
func buildEmailSender(cfg config.AppConfig) email.Sender {
	provider := strings.ToLower(strings.TrimSpace(os.Getenv("EMAIL_PROVIDER")))
	if provider == "" {
		provider = "ses" // back-compat default
	}
	switch provider {
	case "noop":
		log.Printf("INFO: EMAIL_PROVIDER=noop, email features will return 503")
		return email.NewNoopSender()
	case "resend":
		return buildResendSender(cfg)
	case "ses":
		return buildSESSender(cfg)
	default:
		log.Printf("WARN: unknown EMAIL_PROVIDER=%q, falling back to noop", provider)
		return email.NewNoopSender()
	}
}

func buildSESSender(cfg config.AppConfig) email.Sender {
	keyID := os.Getenv("SES_ACCESS_KEY_ID")
	secret := os.Getenv("SES_SECRET_ACCESS_KEY")
	region := os.Getenv("SES_REGION")
	if keyID == "" || secret == "" {
		log.Printf("INFO: SES not configured, email features will return 503")
		return email.NewNoopSender()
	}
	if region == "" {
		region = "us-east-1"
	}
	from := envOr("SES_FROM_ADDRESS", envOr("EMAIL_FROM_ADDRESS", cfg.EmailFromAddress))
	fromName := envOr("SES_FROM_NAME", envOr("EMAIL_FROM_NAME", cfg.EmailFromName))
	sender, err := email.NewSESSender(email.SESConfig{
		AccessKeyID:      keyID,
		SecretAccessKey:  secret,
		Region:           region,
		DefaultFrom:      from,
		DefaultFromName:  fromName,
		ConfigurationSet: os.Getenv("SES_CONFIGURATION_SET"),
		SendsPerSecond:   14,
	})
	if err != nil {
		log.Printf("WARN: SES sender init failed (%v); falling back to noop", err)
		return email.NewNoopSender()
	}
	log.Printf("INFO: SES sender configured (region=%s from=%s)", region, from)
	return sender
}

func buildResendSender(cfg config.AppConfig) email.Sender {
	apiKey := os.Getenv("RESEND_API_KEY")
	if apiKey == "" {
		log.Printf("INFO: RESEND_API_KEY not set, email features will return 503")
		return email.NewNoopSender()
	}
	from := envOr("RESEND_FROM_ADDRESS", envOr("EMAIL_FROM_ADDRESS", cfg.EmailFromAddress))
	fromName := envOr("RESEND_FROM_NAME", envOr("EMAIL_FROM_NAME", cfg.EmailFromName))
	rate := 2 // free tier; production tier raises via RESEND_RATE_PER_SEC
	if v := os.Getenv("RESEND_RATE_PER_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			rate = n
		}
	}
	sender, err := email.NewResendSender(email.ResendConfig{
		APIKey:          apiKey,
		DefaultFrom:     from,
		DefaultFromName: fromName,
		SendsPerSecond:  rate,
	})
	if err != nil {
		log.Printf("WARN: Resend sender init failed (%v); falling back to noop", err)
		return email.NewNoopSender()
	}
	log.Printf("INFO: Resend sender configured (from=%s rate=%d/s)", from, rate)
	return sender
}

// buildStorageService constructs the R2-backed storage service. Both R2
// access and a non-nil sqlStore are required; if either is missing we
// return nil and main.go skips mounting the /storage routes. Quota
// defaults match what's documented in OPERATOR.md / values-prod.yaml.
func buildStorageService(cfg config.AppConfig, sqlStore storagesvc.BucketStore) *storagesvc.Service {
	keyID := os.Getenv("R2_ACCESS_KEY_ID")
	secret := os.Getenv("R2_SECRET_ACCESS_KEY")
	endpoint := os.Getenv("R2_ENDPOINT")
	bucket := os.Getenv("R2_BUCKET")
	if keyID == "" || secret == "" || endpoint == "" || bucket == "" {
		log.Printf("INFO: R2 not configured, storage feature disabled")
		return nil
	}
	r2, err := storagesvc.NewR2Client(storagesvc.R2Config{
		AccessKeyID:     keyID,
		SecretAccessKey: secret,
		Endpoint:        endpoint,
		Region:          os.Getenv("R2_REGION"),
		Bucket:          bucket,
		PublicURL:       cfg.StoragePublicURL,
	})
	if err != nil {
		log.Printf("WARN: R2 client init failed: %v", err)
		return nil
	}
	// Per-tier quotas — kept here rather than in config.AppConfig because
	// they're rarely changed and tied to product copy. Operators can
	// override via a v1.2 config knob if needed.
	tierQuotas := map[string]int64{
		"free":       1 * 1024 * 1024 * 1024,        // 1 GiB
		"standard":   50 * 1024 * 1024 * 1024,       // 50 GiB
		"enterprise": 1 * 1024 * 1024 * 1024 * 1024, // 1 TiB
	}
	log.Printf("INFO: R2 storage configured (endpoint=%s bucket=%s)", endpoint, bucket)
	return storagesvc.NewService(sqlStore, r2, tierQuotas)
}
