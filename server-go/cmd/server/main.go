package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/bootstrap"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/docbrowser"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/metrics"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/natsauth"
	"github.com/excalibase/provisioning-poc/internal/projectdb"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/scheduler"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/excalibase/provisioning-poc/internal/wiring"
	"github.com/excalibase/provisioning-poc/pkg/kmsseal"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/nats-io/nats.go"
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
	pgStore, err := buildParameterGroupStore(cfg)
	if err != nil {
		log.Fatalf("parameter-group store: %v", err)
	}

	vc, localVault, vaultCleanup := buildVault(cfg, sqlStore)
	defer vaultCleanup()

	bootstrapDefaultOrgIfNeeded(cfg, sqlStore)

	k8sClient := buildK8sClient(cfg)
	if err := verifyAppRuntime(context.Background(), cfg, k8sClient); err != nil {
		log.Fatalf("app hosting: %v", err)
	}
	factory, dockerClientRef := buildProvisionerFactory(cfg, k8sClient)

	// One way to reach a tenant database, shared by the schema migrator,
	// the cron sync and the scheduler sweep.
	projectDB := projectdb.NewOpener(store, vc, projectdb.OverridesFromEnv(), projectdb.PoolLimits{
		MaxOpenConns:     cfg.ProjectDBMaxOpenConns,
		MaxPools:         cfg.ProjectDBMaxPools,
		StatementTimeout: cfg.ProjectDBStatementTimeout,
		LockTimeout:      cfg.ProjectDBLockTimeout,
	})
	defer projectDB.Close()

	fnHandler := buildFunctionHandler(cfg, vc, store, sqlStore, k8sClient, projectDB)

	provSvc, lifecycleClaimer, provCleanup := buildProvisioningService(cfg, store, sqlStore, factory, k8sClient, vc, dockerClientRef)
	defer provCleanup()
	// Function invocation caches "is this project live" for a few seconds so
	// it does not read the platform database per request; this tells it the
	// moment a project is claimed for teardown.
	provSvc.AddDeletionObserver(fnHandler)
	// A project claimed for teardown must not keep an open pool on a database
	// that is going away.
	provSvc.AddDeletionObserver(projectDB)

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
	// The schema browser holds one connection per project for ten minutes.
	// An open session stops the tenant's Postgres shutting down, so the pod
	// survives, the namespace will not terminate, and the teardown times out
	// waiting for it (EXC-431).
	provSvc.AddDeletionObserver(deps.schemaHandler)

	// How a customer reaches their database from outside the cluster
	// (EXC-410). Nil when the platform offers no public endpoints, and
	// every caller then carries no endpoint step at all.
	dbEndpointSvc := buildDBEndpointService(cfg, sqlStore, store, k8sClient)
	var dbEndpoints service.PublicEndpointReconciler
	if dbEndpointSvc != nil {
		dbEndpoints = dbEndpointSvc
		deps.provHandler.SetDBEndpointService(dbEndpointSvc)
		provSvc.SetPublicEndpointReconciler(dbEndpointSvc)
	}

	stopCallout := startNatsAuthCallout(cfg, sqlStore)
	defer stopCallout()

	policyPub, err := service.NewPolicyChangePublisher(cfg.NatsURL, provisioningNatsOptions(cfg)...)
	if err != nil {
		log.Printf("WARN: policy change publisher: %v", err)
	} else {
		defer policyPub.Close()
		deps.rlsPolicyHandler.SetPublisher(policyPub)
		// Grant + enforcement writes ride the same subject so the engine
		// evicts cached policies and grants together (EXC-370).
		deps.tableGrantHandler.SetPublisher(policyPub)
		// DDL reshapes the schema the engine caches for thirty minutes, so it
		// rides the same subject (EXC-437).
		deps.schemaHandler.SetPublisher(policyPub)
		provSvc.SetProjectEventPublisher(policyPub)
	}

	scheduler, schedulerStop := startBackupScheduler(cfg, sqlStore, deps.backupHandler)
	defer schedulerStop()
	if scheduler != nil {
		deps.backupHandler.SetScheduler(scheduler)
	}

	fnSchedHandles := startFunctionScheduler(cfg, sqlStore, fnHandler, projectDB)
	defer fnSchedHandles.Stop()

	stopReplayer := startFunctionReplayer(cfg, fnHandler)
	defer stopReplayer()

	// Wire pause/resume — backup must run before pause, so PauseService
	// depends on the BackupService that backupHandler exposes.
	pausers := buildPausers(cfg, factory, dockerClientRef)
	var pauseSvc *service.PauseService
	if len(pausers) > 0 {
		pauseSvc = service.NewPauseService(service.PauseServiceConfig{
			Instances: store,
			Pausers:   pausers,
			Backups:   deps.backupHandler.Service(),
			// A resume puts the CDC watcher back only after the primary is
			// serving; the control plane holds its credentials (EXC-363).
			Replication: provSvc,
			Poller:      service.NewPausePoller(cfg.PauseTimeout),
			// The same per-project lease deletion takes, so one project is
			// only ever under one lifecycle operation at a time (EXC-403).
			Claimer: lifecycleClaimer,
			// A paused project has no public Service and refuses
			// connections; a resume brings it back on the same port.
			Endpoints: dbEndpoints,
		})
		// A paused project's database is down for as long as it stays paused;
		// its pool must not keep connections open against it.
		pauseSvc.AddStatusObserver(projectDB)
		deps.provHandler.SetPauseService(pauseSvc)
		deps.provHandler.SetInstanceStore(store)
	}
	stopIdlePause := startIdlePauseScheduler(cfg, sqlStore, store, provSvc, pauseSvc, deps.emailSender)
	defer stopIdlePause()

	_, stopStorageReap := startStorageReaper(cfg, sqlStore, deps.storageSvc)
	defer stopStorageReap()

	if sqlStore != nil {
		stopRestoreSweeper := wireRestoreOrchestrator(cfg, sqlStore, store, deps)
		defer stopRestoreSweeper()
	}

	checkFeatureWiring(cfg, wiring.Deps{
		SchedulerInvoker:  fnHandler != nil,
		SchedulerProjects: projectDB != nil,
		ProjectDB:         projectDB != nil,
		// functionCronLock always yields a claim: the platform advisory lock
		// in cloud, a no-op one in a single-process deployment.
		CronLeader:      true,
		PauseService:    pauseSvc != nil,
		FunctionRuntime: fnHandler != nil,
	})

	r := buildRouter(cfg, sqlStore, store, deps)

	startServer(cfg, r)
}

// checkFeatureWiring stops the process when a switched-on feature is
// missing a dependency it cannot work without. Run once, after everything
// is built and before anything is served, so a half-wired deployment fails
// at startup instead of answering requests that quietly do nothing.
func checkFeatureWiring(cfg config.AppConfig, deps wiring.Deps) {
	if err := wiring.Check(wiring.Features(cfg, deps)); err != nil {
		log.Fatal(err)
	}
}

// wireRestoreOrchestrator builds the restore orchestrator, registers the
// single delegate-to-adapter step, sweeps abandoned jobs, starts the periodic
// sweeper, and wires it into the backup handler. Extracted from runServer to
// keep that function's branching shallow. Returns the sweeper's stop.
func wireRestoreOrchestrator(cfg config.AppConfig, sqlStore storage.PlatformStore, store storage.InstanceStore, deps *handlerDeps) func() {
	orchestrator := service.NewRestoreOrchestrator(service.RestoreOrchestratorConfig{
		Jobs: sqlStore.RestoreJobs(),
		// A swept job's target project is told its restore was interrupted,
		// so the user reading the project sees why it is stuck.
		Instances: store,
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
	if err := orchestrator.SweepStale(context.Background()); err != nil {
		log.Printf("WARN: restore boot sweep: %v", err)
	}
	deps.backupHandler.SetRestoreOrchestrator(orchestrator)
	// Failing a job whose replica crashed must not wait for the next
	// restart, and only one replica should be doing it.
	var lock storage.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() {
		lock = pgstore.NewAdvisoryLock(sqlStore.DB(), restoreSweepLockID)
	}
	return orchestrator.StartSweeper(context.Background(), service.NewLeadership(lock), restoreSweepInterval)
}

// restoreSweepLockID is the advisory lock the restore sweeper leads on. It
// must stay distinct from every other advisory lock id the platform takes.
const restoreSweepLockID int64 = 0x51c2_7d0e_9a41_3b77

// restoreSweepInterval is how often the leader looks for restores whose
// driver has gone silent. Two sweeps inside the staleness bound is enough:
// the bound, not this, decides how long a job may go unheard.
const restoreSweepInterval = 30 * time.Second

// runRestoreStep resolves the source instance and dispatches the restore to
// the backup service, translating the job's target kind into a RestoreRequest.
func runRestoreStep(ctx context.Context, store storage.InstanceStore, backupSvc *service.BackupService, j *domain.RestoreJob) error {
	inst, err := store.FindByProjectID(j.SourceProjectID)
	if err != nil || inst == nil {
		return fmt.Errorf("source project %s not found", j.SourceProjectID)
	}
	req := domain.RestoreRequest{
		TargetProjectID: j.NewProjectID,
		NewProjectName:  j.NewProjectName,
	}
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

// Advisory-lock keys, one per scheduled sweep. Each is FNV-1a of the
// scheduler's name, and they must stay distinct: two sweeps sharing a key
// would mean whichever replica claimed it first silently stops the other from
// ever running. Positive so the value survives any signed/unsigned handling
// on the way to pg_try_advisory_lock.
const (
	backupSchedulerLockID int64 = 0x6168_0acb_4233_4b21
	idlePauseLockID       int64 = 0x6168_0acb_1d1e_9a05
	storageReapLockID     int64 = 0x6168_0acb_7c41_35d3
)

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
	var lock storage.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() {
		lock = pgstore.NewAdvisoryLock(sqlStore.DB(), backupSchedulerLockID)
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

// startIdlePauseScheduler boots the EXC-280 sweep: every hour, ACTIVE projects
// on tiers with autoPauseAfterDays = N > 0 are warned after N-1 idle days and
// paused (pre-pause backup included, via PauseService) after N. Cloud replicas
// elect a leader through a Postgres advisory lock so only one sweeps per tick.
// Gated by EXCALIBASE_AUTOPAUSE_ENABLED (default: on in cloud, off self-hosted).
func startIdlePauseScheduler(
	cfg config.AppConfig,
	sqlStore storage.PlatformStore,
	store storage.InstanceStore,
	provSvc *service.ProvisioningService,
	pauseSvc *service.PauseService,
	sender email.Sender,
) func() {
	noop := func() {
		// nothing started, nothing to stop
	}
	if !cfg.AutoPauseEnabled {
		log.Println("Idle auto-pause disabled (EXCALIBASE_AUTOPAUSE_ENABLED)")
		return noop
	}
	if pauseSvc == nil || sqlStore == nil {
		log.Println("WARN: idle auto-pause enabled but no pause service is wired — sweep not started")
		return noop
	}
	var lock storage.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() {
		lock = pgstore.NewAdvisoryLock(sqlStore.DB(), idlePauseLockID)
	}
	scheduler := service.NewIdlePauseScheduler(service.IdlePauseSchedulerConfig{
		Instances: store,
		Activity:  sqlStore,
		Tiers:     provSvc.TierConfig,
		Pauser:    pauseSvc,
		// A project left in RESUMING has its database up and no CDC, and
		// nothing else would ever notice (EXC-403).
		Resumer: pauseSvc,
		Audit:   sqlStore,
		Lock:    lock,
		Notifier: service.NewIdleWarnEmail(service.IdleWarnEmailConfig{
			Users:        sqlStore,
			Sender:       sender,
			ProductName:  cfg.EmailProductName,
			DashboardURL: studioURL(cfg),
		}),
	})
	scheduler.Start(context.Background())
	log.Printf("Idle auto-pause sweep started (every %s; warning at N-1 days, pause at N per tier)", service.DefaultIdlePauseInterval)
	return scheduler.Stop
}

// storageReapGrace is how long an object with no catalogue row is left alone
// before the sweep treats it as an abandoned upload. It must outlast a signed
// URL plus the round trip a client needs to confirm; STORAGE_REAP_GRACE (a Go
// duration, e.g. "6h") raises it for operators whose clients take longer. An
// unparseable value is fatal rather than silently replaced — an operator who
// set it meant it.
func storageReapGrace() time.Duration {
	raw := os.Getenv("STORAGE_REAP_GRACE")
	if raw == "" {
		return storagesvc.DefaultUnconfirmedGrace
	}
	grace, err := time.ParseDuration(raw)
	if err != nil || grace <= 0 {
		log.Fatalf("STORAGE_REAP_GRACE must be a positive Go duration, got %q", raw)
	}
	return grace
}

// startStorageReaper boots the sweep that deletes objects which reached the
// blob plane but were never confirmed. They carry no catalogue row, so quota
// accounting cannot see them and no other path can even name them: without
// this sweep a caller could take an upload URL, PUT to it, never confirm, and
// repeat, without ever meeting a limit.
//
// Returns nil when storage is not configured — there is no blob plane to
// sweep, so the sweep does not exist rather than running and failing. Cloud
// replicas elect a leader on their own advisory key so only one sweeps.
func startStorageReaper(cfg config.AppConfig, sqlStore storage.PlatformStore, storageSvc *storagesvc.Service) (*service.StorageReaper, func()) {
	noop := func() {
		// nothing started, nothing to stop
	}
	if storageSvc == nil {
		return nil, noop
	}
	var lock storage.LeaderLock = service.AlwaysLeader{}
	if cfg.IsCloud() && sqlStore != nil {
		lock = pgstore.NewAdvisoryLock(sqlStore.DB(), storageReapLockID)
	}
	grace := storageReapGrace()
	reaper := service.NewStorageReaper(service.StorageReaperConfig{
		Storage: storageSvc,
		Lock:    lock,
		Grace:   grace,
	})
	reaper.Start(context.Background())
	log.Printf("Storage reaper started (every %s; unconfirmed uploads collected after %s)",
		service.DefaultStorageReapInterval, grace)
	return reaper, reaper.Stop
}

// studioURL is the first concrete CORS origin — the Studio the operator
// serves — used as the dashboard link in owner-facing emails. Empty when the
// allowlist is a wildcard.
func studioURL(cfg config.AppConfig) string {
	for _, origin := range cfg.CORSOrigins {
		if strings.HasPrefix(origin, "http") {
			return origin
		}
	}
	return ""
}

// functionCronLockID is the advisory lock the cron half of the function
// scheduler leads on. Cron enqueues are decided from last_enqueued_at, not
// claimed with a row lock, so two replicas walking the registry in the same
// minute would both insert; the task half needs no lock (FOR UPDATE SKIP
// LOCKED already keeps two workers off one row).
const functionCronLockID int64 = 0x6168_0acb_5c7a_11e6

// startFunctionScheduler boots the per-project sweep that drains deferred
// tasks and enqueues due cron jobs. Both live in each tenant's own database
// — the runtime writes them there — so the sweep walks every servable
// project rather than a single pool. An enabled scheduler that cannot be
// fully wired stops the process instead of running nothing.
func startFunctionScheduler(
	cfg config.AppConfig,
	sqlStore storage.PlatformStore,
	fnHandler *handler.FunctionHandler,
	projectDB *projectdb.Opener,
) *bootstrap.SchedulerHandles {
	boot := bootstrap.SchedulerBootConfig{
		Enabled:          cfg.SchedulerEnabled,
		PollInterval:     cfg.SchedulerPollInterval,
		CronPollInterval: cfg.SchedulerCronInterval,
		CronLeader:       service.NewLeadership(functionCronLock(cfg, sqlStore)),
		Limits:           schedulerLimits(cfg),
	}
	if fnHandler != nil {
		boot.Invoker = fnHandler.SchedulerInvoker()
		boot.Functions = fnHandler.SchedulerFunctions()
	}
	if projectDB != nil {
		boot.Projects = projectDB.ServableProjectIDs
		boot.ProjectDB = projectDB.Open
	}
	handles, err := bootstrap.StartScheduler(context.Background(), boot)
	if err != nil {
		log.Fatalf("function scheduler: %v", err)
	}
	if handles.Started() {
		log.Printf("Function scheduler started (poll=%v, cronPoll=%v)",
			cfg.SchedulerPollInterval, cfg.SchedulerCronInterval)
	} else {
		log.Println("Function scheduler disabled via EXCALIBASE_SCHEDULER_ENABLED")
	}
	return handles
}

// schedulerLimits carries the operator's bounds on tenant-written work into
// the sweep.
func schedulerLimits(cfg config.AppConfig) scheduler.Limits {
	return scheduler.Limits{
		Batch:              cfg.SchedulerBatch,
		ProjectConcurrency: cfg.SchedulerProjectConcurrency,
		GlobalConcurrency:  cfg.SchedulerGlobalConcurrency,
		MaxArgsBytes:       cfg.SchedulerMaxArgsBytes,
		MaxAttempts:        cfg.SchedulerMaxAttempts,
		CronMinInterval:    cfg.CronMinInterval,
		CronMaxJobs:        cfg.CronMaxJobsPerProject,
		ProjectTimeout:     cfg.SchedulerProjectTimeout,
		ClaimLease:         cfg.SchedulerClaimLease,
	}
}

// functionCronLock is the platform advisory lock in cloud (several
// replicas) and a no-op claim self-hosted (one process).
func functionCronLock(cfg config.AppConfig, sqlStore storage.PlatformStore) storage.LeaderLock {
	if cfg.IsCloud() && sqlStore != nil && sqlStore.DB() != nil {
		return pgstore.NewAdvisoryLock(sqlStore.DB(), functionCronLockID)
	}
	return service.AlwaysLeader{}
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
	internalEmail      *handler.InternalEmailHandler
	storageHandler     *handler.StorageHandler
	// documentsHandler is Studio's DocumentDB document browser; nil without
	// Kubernetes, which is where DocumentDB projects run.
	documentsHandler  *handler.DocumentBrowserHandler
	adminHandler      *handler.AdminHandler
	authHandler       *handler.AuthHandler
	svcAcctHandler    *handler.ServiceAccountHandler
	orgHandler        *handler.OrgHandler
	vaultHandler      *handler.VaultHandler
	schemaHandler     *handler.SchemaHandler
	realtimeHandler   *handler.RealtimeHandler
	fnHandler         *handler.FunctionHandler
	rlsPolicyHandler  *handler.RlsPolicyHandler
	tableGrantHandler *handler.TableGrantHandler
	appHandler        *handler.AppHandler
	appDeployHandler  *handler.AppDeployHandler
	appSecretHandler  *handler.AppSecretHandler
	tierHandler       *handler.TierHandler
	pgCatalogHandler  *handler.PostgresCatalogHandler
	capDeps           *capacityDeps
	rlUnauth          func(http.Handler) http.Handler
	rlAuthed          func(http.Handler) http.Handler
	rlDataPlane       func(http.Handler) http.Handler
	// rlMailSend bounds the routes that make the platform send mail. It is far
	// tighter than rlAuthed because the cost of overuse is not our CPU, it is
	// the sending domain's reputation.
	rlMailSend func(http.Handler) http.Handler
	// activity marks a project as seen on every successful project-scoped
	// call (EXC-279). Mounted after the access guards so rejected calls never
	// count.
	activity func(http.Handler) http.Handler
	// emailSender is shared with post-construction wiring (idle-pause warnings).
	emailSender email.Sender
	// storageSvc is nil when R2 is not configured; the reaper is started
	// from it after the handlers are built.
	storageSvc *storagesvc.Service
}

// startFunctionReplayer boots the EXC-337 cold-start replay loop: it polls
// every project's Deno runtime and re-deploys the project's functions from
// the store whenever the runtime reports a new bootId (pod restart). Returns
// a stop function that blocks until the loop has exited. Disable with
// EXCALIBASE_FN_REPLAY_ENABLED=false; tune with EXCALIBASE_FN_REPLAY_POLL_MS.
func startFunctionReplayer(appCfg config.AppConfig, fnHandler *handler.FunctionHandler) func() {
	noop := func() {
		// nothing started, nothing to stop
	}
	if !appCfg.FnReplayEnabled {
		log.Println("Function replay disabled via EXCALIBASE_FN_REPLAY_ENABLED")
		return noop
	}
	cfg := edgefn.ReplayConfig{Interval: appCfg.FnReplayPollInterval}
	replayer, err := fnHandler.NewReplayer(cfg)
	if err != nil {
		log.Printf("WARN: function replay disabled: %v", err)
		return noop
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		replayer.Run(ctx)
	}()
	log.Printf("Function replay on runtime cold start enabled (poll every %s)", cfg.Interval)
	return func() {
		cancel()
		<-done
	}
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
func buildFunctionHandler(
	cfg config.AppConfig,
	vc vaultclient.VaultClient,
	store storage.InstanceStore,
	sqlStore storage.PlatformStore,
	k8sClient k8s.KubeClient,
	projectDB *projectdb.Opener,
) *handler.FunctionHandler {
	fnStore := buildFunctionStore(cfg, sqlStore)
	fnSecrets := edgefn.NewSecretsStore(vc)
	fnClient := edgefn.NewRuntimeClient(cfg.DenoRuntimeURL, cfg.DenoRuntimeSecret)
	fnHandler := handler.NewFunctionHandler(fnStore, fnSecrets, fnClient, store, sqlStore, cfg.PublicBaseURL)
	if cfg.ProvisionerMode != "docker" {
		fnHandler.SetK8sClient(k8sClient, cfg.DenoRuntimeImage, cfg.DenoRuntimeSecret)
	}
	fnHandler.SetVault(vc)
	// Without this resolver a deploy stores the declared schema and never
	// applies it, and schema/apply answers 503 — the platform's own way of
	// opening a project database is the one wired here.
	if projectDB != nil {
		fnHandler.SetProjectDBFn(projectDB.Open)
	}
	fnHandler.SetAutoMigrate(cfg.AutoMigrate)
	// EXC-11: end-user tokens must name this project in their aud claim.
	fnHandler.SetAudienceRequirement(cfg.JWTRequireAud, cfg.JWTAudPrefix)
	wireFunctionEgress(cfg, sqlStore, fnHandler)
	return fnHandler
}

// wireFunctionEgress attaches the per-project outbound allowlist store and the
// operator default list (EXC-348). The store is the platform Postgres in every
// mode; a malformed EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS stops the server rather
// than silently widening (or narrowing) every project's egress.
func wireFunctionEgress(cfg config.AppConfig, sqlStore storage.PlatformStore, fnHandler *handler.FunctionHandler) {
	if pg, ok := sqlStore.(*pgstore.Store); ok {
		fnHandler.SetEgressStore(edgefn.NewPostgresEgressStore(pg.DB()))
	} else {
		log.Println("WARN: no Postgres platform store — the edge-function egress allowlist API is unavailable")
	}
	defaults, err := edgefn.ParseEgressHostList(cfg.FnEgressDefaultHosts)
	if err != nil {
		log.Fatalf("EXCALIBASE_FN_EGRESS_DEFAULT_HOSTS: %v", err)
	}
	if len(defaults) > 0 {
		log.Printf("Edge functions: operator egress default = %v", defaults)
	}
	fnHandler.SetEgressDefaults(defaults)
}

// wireProjectCors attaches the per-project browser-origin allowlist store
// (EXC-23). It lives in the platform Postgres; without it the /cors API is
// unavailable and /info reports no origins, so the data plane sends no CORS
// headers rather than guessing.
func wireProjectCors(sqlStore storage.PlatformStore, provHandler *handler.ProvisioningHandler) {
	if corsStore, ok := sqlStore.(storage.ProjectCorsStore); ok {
		provHandler.SetCorsStore(corsStore)
		return
	}
	log.Println("WARN: no Postgres platform store — the per-project CORS allowlist API is unavailable")
}

// wireProjectAuthSettings attaches the per-project auth settings store
// (EXC-367). It lives in the platform Postgres; without it the
// /auth-settings API is unavailable and /info reports the zero value
// (verification off, no site URL) rather than guessing.
func wireProjectAuthSettings(sqlStore storage.PlatformStore, provHandler *handler.ProvisioningHandler) {
	if authStore, ok := sqlStore.(storage.ProjectAuthSettingsStore); ok {
		provHandler.SetAuthSettingsStore(authStore)
		return
	}
	log.Println("WARN: no Postgres platform store — the per-project auth settings API is unavailable")
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
) (*service.ProvisioningService, service.ProjectOperationClaimer, func()) {
	var lifecycleClaimer service.ProjectOperationClaimer
	provSvc := service.NewProvisioningService(store, factory, k8sClient)
	provSvc.SetVault(vc)
	// A rotated password is only good if the database accepts it, and the
	// database is the only thing that can say so.
	provSvc.SetCredentialVerifier(service.NewTenantRoleVerifier())
	provSvc.SetOrgStore(sqlStore)
	provSvc.SetTierStore(sqlStore)
	provSvc.SetSelfHostedMode(!cfg.IsCloud())
	provSvc.SetCapacityHeadroom(cfg.CapacityHeadroomPercent)
	if cfg.ProvisionerMode == "docker" {
		provSvc.SetDefaultDeploymentMode(domain.ModeDocker)
	} else {
		provSvc.SetDefaultDeploymentMode(domain.ModeK8s)
	}

	// Several control-plane replicas share one platform database, so the
	// lifecycle lease has to be visible to all of them: a per-project
	// Postgres advisory lock, released automatically if the holder's
	// connection dies. Deletion, pause and resume all take it, so one
	// project can only be under one lifecycle operation at a time.
	if pg, ok := sqlStore.(*pgstore.Store); ok {
		lifecycleClaimer = service.NewAdvisoryOperationClaimer(
			func(key int64) storage.LeaderLock { return pgstore.NewAdvisoryLock(pg.DB(), key) })
		provSvc.SetOperationClaimer(lifecycleClaimer)
	}

	provSvc.SetBackupDefaults(backupDefaults(cfg))
	// Deprovision with confirmDeleteBackups resolves the store through
	// provSvc.BackupStorage() — the same source backups are written with.
	provSvc.SetBackupPurger(service.NewBackupPurger(provSvc, dockerBackupKeyPrefix,
		service.AWSObjectDeleterFactory(backupUsePathStyle())))
	if name := os.Getenv("REALTIME_PUBLICATION_NAME"); name != "" {
		provSvc.SetPublicationName(name)
	}
	if dockerClientRef != nil {
		provSvc.SetDockerClient(dockerClientRef)
	}
	provSvc.SetLokiURL(cfg.LokiURL)

	wireNatsCredentialMinter(sqlStore, provSvc)

	cleanup := wirePgDogNotifier(cfg, sqlStore, provSvc)
	return provSvc, lifecycleClaimer, cleanup
}

// wireNatsCredentialMinter lets provisioning issue project-scoped bus
// credentials (EXC-324). Without a Postgres-backed store there is nowhere to
// persist the hashes, so watchers stay unauthenticated.
func wireNatsCredentialMinter(sqlStore storage.PlatformStore, provSvc *service.ProvisioningService) {
	credStore, ok := sqlStore.(storage.NatsCredentialStore)
	if !ok {
		return
	}
	provSvc.SetNatsCredentialMinter(service.NewNatsCredentialMinter(credStore))
}

// startNatsAuthCallout answers the NATS server's auth_callout requests. It is
// what makes every other principal's credential mean anything: without it a
// server configured for callout refuses every connection.
func startNatsAuthCallout(cfg config.AppConfig, sqlStore storage.PlatformStore) func() {
	noop := func() {
		// no-op cleanup: the responder was never started.
	}
	if cfg.NatsURL == "" || cfg.NatsCalloutIssuerSeed == "" {
		log.Println("NATS auth callout disabled (no issuer seed)")
		return noop
	}
	credStore, ok := sqlStore.(storage.NatsCredentialStore)
	if !ok {
		log.Println("WARN: NATS auth callout needs the Postgres platform store; not started")
		return noop
	}
	// The chart mints the service passwords into a Secret; the callout reads
	// hashes from the database, so each boot re-derives them from the Secret.
	if err := natsauth.SeedServicePrincipals(context.Background(), credStore, map[string]string{
		natsauth.PrincipalProvisioning: cfg.NatsPassword,
		natsauth.PrincipalGraphQL:      cfg.NatsGraphQLPassword,
		natsauth.PrincipalPgDog:        cfg.NatsPgDogPassword,
	}); err != nil {
		log.Printf("WARN: NATS service credentials not seeded: %v", err)
	}

	responder, err := natsauth.NewResponder(credStore, cfg.NatsCalloutIssuerSeed, cfg.NatsCalloutAccount, cfg.NatsCDCStream)
	if err != nil {
		log.Printf("WARN: NATS auth callout: %v", err)
		return noop
	}
	// The callout user is the delegated auth account's own login, not a
	// permission-matrix principal, so it takes the shared options directly.
	calloutOpts := append(natsauth.BaseOptions(cfg.NatsCalloutUser),
		nats.UserInfo(cfg.NatsCalloutUser, cfg.NatsCalloutPassword))
	conn, err := nats.Connect(cfg.NatsURL, calloutOpts...)
	if err != nil {
		log.Printf("WARN: NATS auth callout connect: %v", err)
		return noop
	}
	if err := responder.Start(conn); err != nil {
		conn.Close()
		log.Printf("WARN: NATS auth callout subscribe: %v", err)
		return noop
	}
	log.Println("NATS auth callout responder listening on " + natsauth.CalloutSubject)
	return func() {
		responder.Close()
		conn.Close()
	}
}

// provisioningNatsOptions are the dial options provisioning's own publishers
// use. A configuration error is fatal for the bus, not for the process: the
// publishers are already best-effort.
func provisioningNatsOptions(cfg config.AppConfig) []nats.Option {
	opts, err := natsauth.ClientOptions(cfg.NatsUser, cfg.NatsPassword, cfg.NatsCDCStream)
	if err != nil {
		log.Printf("WARN: NATS client options: %v", err)
		return nil
	}
	return opts
}

// dockerBackupKeyPrefix is where the Docker backup adapter writes a project's
// objects ({prefix}{projectId}/...). The purge derives its prefix from it.
const dockerBackupKeyPrefix = "backups/"

// backupUsePathStyle: path-style ON by default — works for R2, MinIO,
// LocalStack. Operators targeting real AWS S3 set BACKUP_S3_PATH_STYLE=0 to
// flip to virtual-host addressing.
func backupUsePathStyle() bool {
	return os.Getenv("BACKUP_S3_PATH_STYLE") != "0"
}

// backupDefaults is the single place the platform-wide backup object
// store is read from configuration. Backup (provisioning + Docker
// uploader) and restore (K8s adapter via ProvisioningService.BackupStorage)
// all derive from this one value — there is no separate restore default.
func backupDefaults(cfg config.AppConfig) *service.BackupDefaults {
	return &service.BackupDefaults{
		AccessKeyID:     cfg.BackupAccessKeyID,
		SecretAccessKey: cfg.BackupSecretAccessKey,
		Endpoint:        cfg.BackupEndpoint,
		Bucket:          cfg.BackupBucket,
		Region:          cfg.BackupRegion,
	}
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
	pgdogNotifier, err := service.NewPgDogNotifier(pgStore, cfg.NatsURL, provisioningNatsOptions(cfg)...)
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
// map for the current deployment mode. K8s adapter is always present.
// Docker adapter is added when both ProvisionerMode=docker and the
// platform has R2/S3 credentials available — without a bucket the
// Docker adapter has nowhere to put bytes, so we keep it out and the
// dispatch returns ErrUnsupportedBackupMode for docker projects until
// the operator finishes wiring credentials.
// wireRestoreVerification gives every backup adapter the probe that proves a
// recovered database answers queries, and the budget its readiness wait runs
// on. A restore is COMPLETED only once that probe has succeeded (EXC-401), so
// an adapter that did not take a probe — or a vault the probe cannot read
// through — is a startup failure, not something to discover on the first
// restore a customer runs.
func wireRestoreVerification(backupSvc *service.BackupService, vc vaultclient.VaultClient, readyTimeout time.Duration) error {
	if vc == nil {
		return errors.New("no vault client: the probe cannot read the credentials a restored project is registered with")
	}
	if err := backupSvc.SetDatabaseProbe(service.NewVaultDatabaseProbe(vc)); err != nil {
		return err
	}
	backupSvc.SetRestoreReadyTimeout(readyTimeout)
	return nil
}

func buildBackupService(
	cfg config.AppConfig,
	store storage.InstanceStore,
	sqlStore storage.PlatformStore,
	k8sClient k8s.KubeClient,
	dockerClient provisioner.DockerClient,
	backupStorage service.BackupStorageSource,
) *service.BackupService {
	k8sAdapter := service.NewK8sBackupAdapter(k8sClient, cfg.StoragePath, backupStorage)
	k8sAdapter.SetInstanceStore(store)
	k8sAdapter.SetPublicDomainSuffix(cfg.DBEndpointDomain)
	adapters := map[domain.DeploymentMode]service.BackupAdapter{
		domain.ModeK8s: k8sAdapter,
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
			defaults := backupDefaults(cfg)
			if defaults.AccessKeyID != "" && defaults.SecretAccessKey != "" && defaults.Endpoint != "" {
				uploader, err := service.NewAWSS3Uploader(context.Background(), service.AWSS3UploaderConfig{
					AccessKeyID:     defaults.AccessKeyID,
					SecretAccessKey: defaults.SecretAccessKey,
					Endpoint:        defaults.Endpoint,
					Region:          defaults.Region,
					UsePathStyle:    backupUsePathStyle(),
				})
				if err == nil {
					dockerAdapter := service.NewDockerBackupAdapter(service.DockerBackupAdapterConfig{
						Runner:    runner,
						Uploader:  uploader,
						Records:   sqlStore.BackupRecords(),
						Bucket:    defaults.Bucket,
						KeyPrefix: dockerBackupKeyPrefix,
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

// newAlertHandler wires the stores the platform-wide alert reads scope their
// results with; without them the handler refuses rather than answer unscoped.
func newAlertHandler(svc *service.AlertingService, instances storage.InstanceStore, orgs storage.OrgStore) *handler.AlertHandler {
	h := handler.NewAlertHandler(svc)
	h.SetScope(instances, orgs)
	return h
}

// newSchemaHandler wires the instance store so the schema browser can
// resolve a project's instance row.
func newSchemaHandler(vc vaultclient.VaultClient, instances storage.InstanceStore) *handler.SchemaHandler {
	h := handler.NewSchemaHandler(vc)
	h.SetInstanceStore(instances)
	return h
}

func buildHandlerDeps(a handlerDepsArgs) *handlerDeps {
	cfg, store, sqlStore := a.cfg, a.store, a.sqlStore
	k8sClient, vc, localVault := a.k8sClient, a.vc, a.localVault
	provSvc, pgStore := a.provSvc, a.pgStore

	metricsSvc := service.NewMetricsService(store, k8sClient, cfg.StoragePath)
	backupSvc := buildBackupService(a.cfg, store, sqlStore, k8sClient, a.dockerClient, provSvc)
	// A restore finishes the way a provision does: the adapters hand the
	// recovered database to the provisioning service's registration path
	// (EXC-366) instead of writing a half-project row themselves.
	backupSvc.SetProjectRegistrar(provSvc)
	// A restore creates a project, so it is metered against the organisation's
	// tier limit exactly as a provision is (EXC-421).
	backupSvc.SetOrgProjectCapacity(provSvc)
	if err := wireRestoreVerification(backupSvc, vc, cfg.RestoreReadyTimeout); err != nil {
		log.Fatalf("restore verification: %v", err)
	}
	perfSvc := service.NewPerformanceService(store, k8sClient)
	auditSvc := service.NewAuditService(store, k8sClient)
	snapshotSvc := service.NewSnapshotService(store, k8sClient, cfg.StoragePath)
	migrationSvc := service.NewMigrationService(store, vc, cfg.StoragePath)
	alertSvc := service.NewAlertingService(cfg.StoragePath)
	setupSvc := service.NewOperatorSetupService(k8sClient)

	emailSender := buildEmailSender(cfg)
	storageSvc := buildStorageService(cfg, sqlStore)
	var storageHandler *handler.StorageHandler
	if storageSvc != nil {
		// A deleted project's files are cleared by the teardown itself:
		// afterwards no row, bucket or endpoint names them. Left unwired when
		// R2 is not configured, so the teardown carries no purge step.
		provSvc.SetObjectPurger(storageSvc)
		storageHandler = handler.NewStorageHandler(storageSvc, store)
		// Phase 10: ctx.storage internal routes share the Deno runtime
		// secret. Empty value disables the routes (all calls 401).
		storageHandler.SetRuntimeSecret(cfg.DenoRuntimeSecret)
		// Resumable/multipart uploads (tus) over the same R2 backend. Skipped
		// silently when R2 isn't configured (TusComposer returns nil).
		if err := storageHandler.EnableResumableUploads(storageSvc.TusComposer()); err != nil {
			log.Printf("WARN: resumable uploads disabled: %v", err)
		} else {
			log.Printf("INFO: resumable (tus) uploads enabled at /api/projects/{projectId}/storage/tus")
		}
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
	authHandler.SetAuditLog(sqlStore)
	authHandler.SetInstanceStore(store)
	authHandler.SetInviteOnly(cfg.RegistrationMode == "invite")
	authHandler.SetSetupTokenStore(sqlStore)
	logFirstAdminSetupToken(sqlStore)

	var vaultHandler *handler.VaultHandler
	if localVault != nil {
		vaultHandler = handler.NewVaultHandler(localVault)
		// The engine and the auth service fetch tenant credentials here; a
		// project the platform must not serve must not answer (EXC-401).
		vaultHandler.SetInstanceStore(store)
		// A human caller's read is bound to the project the path names, which
		// needs the membership lookup as well as the project row (EXC-418).
		vaultHandler.SetOrgStore(sqlStore)
	}

	realtimeHandler := handler.NewRealtimeHandler(sqlStore, sqlStore, vc)
	if name := os.Getenv("REALTIME_PUBLICATION_NAME"); name != "" {
		realtimeHandler.SetPublicationName(name)
	}
	provHandler := handler.NewProvisioningHandler(provSvc, sqlStore)
	provHandler.SetActivityStore(sqlStore)
	wireProjectCors(sqlStore, provHandler)
	wireProjectAuthSettings(sqlStore, provHandler)
	activityRecorder := service.NewActivityRecorder(service.ActivityRecorderConfig{Store: sqlStore})
	// Newly registered projects (provisioned or restored) get a last-seen
	// marker straight away so idle-pause never sees them as stale.
	provSvc.SetActivityRecorder(activityRecorder)

	return &handlerDeps{
		provHandler:        provHandler,
		metricsHandler:     handler.NewMetricsHandler(metricsSvc),
		backupHandler:      handler.NewBackupHandler(backupSvc),
		perfHandler:        handler.NewPerformanceHandler(perfSvc),
		auditHandler:       handler.NewAuditHandler(auditSvc),
		snapshotHandler:    handler.NewSnapshotHandler(snapshotSvc),
		migrationHandler:   handler.NewMigrationHandler(migrationSvc),
		alertHandler:       newAlertHandler(alertSvc, store, sqlStore),
		setupHandler:       handler.NewSetupHandler(setupSvc),
		pgHandler:          handler.NewParameterGroupHandler(pgStore),
		emailTokensHandler: emailTokensHandler,
		storageHandler:     storageHandler,
		documentsHandler:   buildDocumentBrowser(k8sClient, vc, store),
		adminHandler:       adminHandler,
		authHandler:        authHandler,
		svcAcctHandler:     handler.NewServiceAccountHandler(sqlStore, sqlStore, sqlStore),
		orgHandler:         newOrgHandler(sqlStore, store),
		vaultHandler:       vaultHandler,
		schemaHandler:      newSchemaHandler(vc, store),
		realtimeHandler:    realtimeHandler,
		rlsPolicyHandler:   handler.NewRlsPolicyHandler(sqlStore.RlsPolicies()),
		tableGrantHandler:  handler.NewTableGrantHandler(sqlStore.TableGrants(), cfg.ExposureEnforced),
		appHandler: handler.NewAppHandler(apphost.NewPostgresAppStore(sqlStore.DB()),
			handler.NewProjectSourceLookup(store), appRoute(cfg).Public()),
		appSecretHandler: handler.NewAppSecretHandler(apphost.NewPostgresAppStore(sqlStore.DB()), vc),
		appDeployHandler: handler.NewAppDeployHandler(service.NewAppDeployService(
			apphost.NewPostgresAppStore(sqlStore.DB()), apphost.NewPostgresDeployStore(sqlStore.DB()),
			k8sClient, store, service.NewAppEnvResolver(vc, store), k8s.AppRenderOptions{
				RuntimeClass: cfg.AppRuntimeClass, ExtraDenyCIDRs: cfg.AppEgressExtraDenyCIDRs, Route: appRoute(cfg),
			})),
		tierHandler:      tierHandler,
		pgCatalogHandler: handler.NewPostgresCatalogHandler(),
		capDeps: &capacityDeps{
			k8sClient:       k8sClient,
			store:           store,
			headroomPercent: cfg.CapacityHeadroomPercent,
			// Same store-backed resolver admission uses, so the capacity report
			// reflects admin edits to tier_configs without a redeploy.
			resolveTier: provSvc.TierConfig,
		},
		rlUnauth:    custommw.RateLimit(custommw.PerIP, 30, time.Minute),
		rlAuthed:    custommw.RateLimit(custommw.PerUser, 600, time.Minute),
		rlDataPlane: custommw.RateLimit(custommw.PerProjectAndUser, 120, time.Second),
		rlMailSend:  custommw.RateLimit(custommw.PerUser, 5, time.Hour),
		activity:    custommw.ProjectActivity(activityRecorder),
		emailSender: emailSender,
		storageSvc:  storageSvc,
		// EXC-11: server-to-server mail relay for excalibase-auth. Its
		// authorization is the capability gate, wired at the mount below.
		internalEmail: handler.NewInternalEmailHandler(emailSender),
	}
}

// routerStores is the slice of the platform store the router itself needs:
// token lookup for ExtractAuth and org membership for the project gates.
// Narrow on purpose so the authz tests can drive the real mounts with fakes.
type routerStores interface {
	auth.TokenLookup
	storage.OrgStore
}

// serveConfig answers what the studio must know before login: the deployment
// mode, and whether app hosting is mounted so it never links to a 404.
func serveConfig(cfg config.AppConfig) http.HandlerFunc {
	body := struct {
		DeploymentMode string `json:"deploymentMode"`
		AppHosting     bool   `json:"appHosting"`
	}{DeploymentMode: cfg.DeploymentMode, AppHosting: cfg.AppHostingEnabled}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(body); err != nil {
			log.Printf("write /api/config: %v", err)
		}
	}
}

// buildRouter wires the chi router with global middleware and mounts every
// API subtree. The per-subtree mounting is delegated to focused helpers so
// this top-level remains a manifest of which features are exposed.
func buildRouter(cfg config.AppConfig, sqlStore routerStores, store storage.InstanceStore, d *handlerDeps) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(metrics.Middleware)
	r.Use(custommw.SecurityHeaders)
	r.Use(custommw.CORS(cfg.CORSOrigins))
	r.Use(auth.ExtractAuth(sqlStore))
	// Capability tokens (the platform's own service principals) are
	// default-deny: mounted here, the gate covers every route including ones
	// added later. Human PATs and sessions pass straight through.
	r.Use(custommw.CapabilityGate)

	handler.RegisterPrometheusHandler(r)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("ok"))
	})
	r.Get("/api/config", serveConfig(cfg))
	r.Get("/api/capacity", auth.RequireAuth(http.HandlerFunc(d.capDeps.serveCapacity)).ServeHTTP)

	mountProvisioningRoutes(r, sqlStore, store, d)
	mountSimpleAuthRoutes(r, sqlStore, store, d)
	mountAuthRoutes(r, d)
	mountOrgAndAdminRoutes(r, cfg, d)
	mountVaultAndSchemaRoutes(r, sqlStore, store, d)
	mountProjectScopedRoutes(r, cfg, sqlStore, store, d)
	mountEmailRoutes(r, d)

	// Phase 7: /http/* dispatch is mounted BEFORE the bare /{fnId} route so
	// chi's router doesn't treat the literal segment "http" as a function id.
	// A successful invoke is end-user traffic, the strongest "this project is
	// alive" signal the platform sees — so it feeds project_activity too.
	r.With(custommw.TenantContext, d.activity).HandleFunc("/functions/v1/{projectId}/http/*", d.fnHandler.PublicHttpInvoke)
	r.With(custommw.TenantContext, d.activity).HandleFunc("/functions/v1/{projectId}/{fnId}", d.fnHandler.PublicInvoke)
	// Internal runtime → provisioning callback for export metadata capture.
	// Authenticates via X-Excalibase-Runtime-Token (shared runtime secret),
	// not JWT — this is server-to-server only.
	r.Post("/internal/runtime/functions/{fnId}/metadata", d.fnHandler.ReceiveExportMetadata)
	// Phase 7: server-to-server bridge used by ctx.runQuery/runMutation/
	// runAction to invoke a sibling function. Same shared-secret auth as
	// the metadata callback above.
	r.With(d.activity).Post("/internal/invoke/{projectId}/{fnId}", d.fnHandler.InternalInvoke)
	return r
}

// mountProvisioningRoutes attaches /api/provision and its per-project sub-router.
func mountProvisioningRoutes(r *chi.Mux, sqlStore storage.OrgStore, store storage.InstanceStore, d *handlerDeps) {
	r.Route("/api/provision", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Get("/", d.provHandler.ListInstances)
		r.Post("/", d.provHandler.Provision)
		r.Post("/estimate", d.provHandler.EstimateCost)

		r.Route("/{projectId}", func(r chi.Router) {
			r.Use(custommw.TenantContext)
			r.Use(custommw.RequireProjectAccess(store, sqlStore))
			r.Use(d.rlDataPlane)
			r.Use(d.activity)

			// Org-role tiers on top of membership (Owner⊇Admin⊇Developer⊇Viewer):
			//   reads = any member (Viewer); writes = Developer; credentials +
			//   destructive lifecycle = Admin. Platform admins bypass. (RBAC gate)
			admin := custommw.RequireProjectRole(domain.OrgRoleAdmin, store, sqlStore)
			dev := custommw.RequireProjectRole(domain.OrgRoleDeveloper, store, sqlStore)

			// Reads — any member.
			r.Get("/", d.provHandler.GetStatus)
			r.Get("/logs", d.provHandler.GetLogs)
			r.Get("/maintenance-window", d.provHandler.GetMaintenanceWindow)

			// Developer+ — a routine write.
			r.With(dev).Put("/maintenance-window", d.provHandler.SetMaintenanceWindow)

			// Admin+ — credentials and destructive lifecycle. Pause/resume stop
			// and restart the tenant workload, so they sit with the lifecycle tier.
			r.With(admin).Delete("/", d.provHandler.Delete)
			r.With(admin).Get("/credentials", d.provHandler.GetCredentials)
			r.With(admin).Post("/credentials/rotate", d.provHandler.RotateCredentials)
			r.With(admin).Patch("/deletion-protection", d.provHandler.SetDeletionProtection)
			r.With(admin).Post("/backups/purge", d.provHandler.PurgeBackups)
			r.With(admin).Post("/pause", d.provHandler.Pause)
			r.With(admin).Post("/resume", d.provHandler.Resume)
			r.With(admin).Post("/upgrade", d.provHandler.UpgradeMinorVersion)

			// Read-only subtrees — any member.
			r.Route("/metrics", func(r chi.Router) { d.metricsHandler.Routes(r) })
			r.Route("/performance", func(r chi.Router) { d.perfHandler.Routes(r) })

			// Developer+ subtrees — schema/data-plane authoring.
			r.Group(func(r chi.Router) {
				r.Use(dev)
				r.Route("/audit", func(r chi.Router) { d.auditHandler.Routes(r) })
				r.Route("/migrations", func(r chi.Router) { d.migrationHandler.Routes(r) })
				r.Route("/rls-policies", func(r chi.Router) { d.rlsPolicyHandler.RlsRoutes(r) })
				r.Route("/column-policies", func(r chi.Router) { d.rlsPolicyHandler.ColumnRoutes(r) })
				r.Route("/table-grants", func(r chi.Router) { d.tableGrantHandler.Routes(r) })
			})

			// Admin+ subtrees — backup/restore and full-DB snapshots (dump +
			// restore are destructive/exfil; kept Admin-only as the safe default).
			r.Group(func(r chi.Router) {
				r.Use(admin)
				r.Route("/backup", func(r chi.Router) { d.backupHandler.Routes(r) })
				r.Route("/snapshot", func(r chi.Router) { d.snapshotHandler.Routes(r) })
			})
		})
	})
}

// mountSimpleAuthRoutes mounts the small auth-gated subtrees (alerts, setup, parameter groups).
func mountSimpleAuthRoutes(r *chi.Mux, sqlStore storage.OrgStore, store storage.InstanceStore, d *handlerDeps) {
	// Read-only tier specs for any authenticated user (the provision page tier
	// selector). Editing stays admin-only under /api/admin/tiers.
	r.Route("/api/tiers", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Get("/", d.tierHandler.List)
	})
	// The PostgreSQL image catalogue, read-only, for the create-project form's
	// version picker and its DocumentDB option. It is served rather than
	// duplicated in the client because both the supported set and the
	// DocumentDB rule move when the catalogue moves.
	r.Route("/api/postgres", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		d.pgCatalogHandler.Routes(r)
	})
	r.Route("/api/alerts", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		d.alertHandler.Routes(r)
		// The per-project read names a tenant: bind it to the caller like every
		// other project route, under the segment where {projectId} exists.
		r.Route("/project/{projectId}", func(r chi.Router) {
			r.Use(custommw.TenantContext)
			r.Use(custommw.RequireProjectAccess(store, sqlStore))
			d.alertHandler.ProjectRoutes(r)
		})
	})
	// /status is unauthenticated (the installer polls it before any credential
	// exists), so RequireAuth sits on the install route inside Routes instead
	// of on the whole subtree, and the per-IP limiter carries the poll.
	r.Route("/api/setup", func(r chi.Router) {
		d.setupHandler.Routes(r, d.rlUnauth)
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
			r.Post("/{tokenHash}/rotate", d.authHandler.RotateToken)
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
		r.Route("/service-accounts", func(r chi.Router) { d.svcAcctHandler.Routes(r) })
	})
}

// mountVaultAndSchemaRoutes mounts the optional vault routes and /api/schema.
// The schema surface is project-scoped: {projectId} is bound at the mount so
// RequireProjectAccess runs with it in scope (EXC-349). Binding the guard one
// level higher — before {projectId} exists — would make it a silent no-op and
// let any authenticated studio user read/modify any tenant's database.
func mountVaultAndSchemaRoutes(r *chi.Mux, sqlStore storage.OrgStore, store storage.InstanceStore, d *handlerDeps) {
	if d.vaultHandler != nil {
		r.Route("/api/vault", func(r chi.Router) { d.vaultHandler.Routes(r) })
	}
	r.Route("/api/schema/{projectId}", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Use(custommw.TenantContext)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		// Viewers may browse (GET); DDL / /query / row writes require Developer+ (RBAC gate).
		r.Use(custommw.RequireProjectRoleForWrites(domain.OrgRoleDeveloper, store, sqlStore))
		r.Use(d.activity)
		r.Use(d.schemaHandler.AnnounceSchemaChange)
		d.schemaHandler.RoutesInner(r)
	})
}

// mountProjectScopedRoutes mounts every /api/projects/{projectId}/* subtree.
func mountProjectScopedRoutes(r *chi.Mux, cfg config.AppConfig, sqlStore storage.OrgStore, store storage.InstanceStore, d *handlerDeps) {
	r.Route("/api/projects/{projectId}/functions", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		r.Use(d.activity)
		// Edge functions are a tenant-developer feature (author + deploy via
		// Studio), so gate on ORG role, not platform perms — a normal developer
		// holds no platform perm and was previously locked out. Reads = any
		// member; deploy / secrets / invoke = Developer+. (End-users run
		// functions via the public /functions/v1 path, not here.)
		dev := custommw.RequireProjectRole(domain.OrgRoleDeveloper, store, sqlStore)
		r.Get("/", d.fnHandler.List)
		r.Get("/_metadata", d.fnHandler.ListExportMetadata)
		r.Get("/runtime/status", d.fnHandler.RuntimeStatus)
		r.With(dev).Post("/", d.fnHandler.Create)
		// Function secrets can hold API keys — Developer+ to read or write.
		r.With(dev).Get("/secrets", d.fnHandler.ListSecrets)
		r.With(dev).Post("/secrets", d.fnHandler.SetSecret)
		r.With(dev).Delete("/secrets/{key}", d.fnHandler.DeleteSecret)
		// Outbound allowlist: it changes what deployed code can reach, so it
		// carries the same Developer+ gate as a deploy (EXC-348).
		r.With(dev).Get("/egress", d.fnHandler.GetEgress)
		r.With(dev).Put("/egress", d.fnHandler.PutEgress)
		r.Route("/{fnId}", func(r chi.Router) {
			r.Get("/", d.fnHandler.Get)
			r.With(dev).Delete("/", d.fnHandler.Delete)
			r.With(dev).Post("/invoke", d.fnHandler.Invoke)
			r.Get("/logs", d.fnHandler.Logs)
		})
	})
	// Customer applications (EXC-378): unmounted, so 404, until the hosting
	// epic (EXC-377) is done. Reads = any member; writes = Developer+.
	if cfg.AppHostingEnabled {
		r.Route("/api/projects/{projectId}/apps", func(r chi.Router) {
			r.Use(custommw.TenantContext)
			r.Use(auth.RequireAuth)
			r.Use(custommw.RequireProjectAccess(store, sqlStore))
			r.Use(d.activity)
			dev := custommw.RequireProjectRole(domain.OrgRoleDeveloper, store, sqlStore)
			r.Get("/", d.appHandler.List)
			r.With(dev).Post("/", d.appHandler.Create)
			r.Route("/{appId}", func(r chi.Router) {
				r.Get("/", d.appHandler.Get)
				r.With(dev).Patch("/", d.appHandler.Update)
				r.With(dev).Delete("/", d.appHandler.Delete)
				r.With(dev).Post("/deploy", d.appDeployHandler.Deploy)
				r.Get("/deploys", d.appDeployHandler.ListDeploys)
				r.With(dev).Post("/deploys/{deployId}/redeploy", d.appDeployHandler.Redeploy)
				r.With(dev).Put("/secrets/{name}", d.appSecretHandler.Set)
			})
		})
	}
	r.Route("/api/projects/{projectId}/schema", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		r.Use(d.activity)
		r.With(custommw.RequireProjectRole(domain.OrgRoleDeveloper, store, sqlStore)).Post("/apply", d.fnHandler.ApplySchemaFromStore)
	})
	r.Route("/api/projects/{projectId}/info", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		r.Use(d.activity)
		r.Get("/", d.provHandler.GetProjectInfo)
	})
	// Browser-origin allowlist (EXC-23): it decides which web apps may call
	// the project's data plane, so reads and writes carry the same Developer+
	// gate as the other data-plane authoring surfaces.
	r.Route("/api/projects/{projectId}/cors", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		r.Use(custommw.RequireProjectRole(domain.OrgRoleDeveloper, store, sqlStore))
		r.Use(d.activity)
		r.Get("/", d.provHandler.GetCors)
		r.Put("/", d.provHandler.PutCors)
	})
	// Public database endpoint (EXC-410): reads report the host, port and
	// cluster CA a client needs to connect and verify, so Developer+ can
	// see them; opening the database to the internet is an admin decision,
	// so writes carry the Admin gate.
	r.Route("/api/projects/{projectId}/db-endpoint", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		r.Use(custommw.RequireProjectRole(domain.OrgRoleDeveloper, store, sqlStore))
		r.Use(custommw.RequireProjectRoleForWrites(domain.OrgRoleAdmin, store, sqlStore))
		r.Use(d.activity)
		r.Get("/", d.provHandler.GetDBEndpoint)
		r.Put("/", d.provHandler.PutDBEndpoint)
	})
	// Auth settings (EXC-367): requireEmailVerification + siteUrl feed the
	// auth service's signup/redirect behavior, so reads and writes carry the
	// same Developer+ gate as the other data-plane authoring surfaces.
	r.Route("/api/projects/{projectId}/auth-settings", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		r.Use(custommw.RequireProjectRole(domain.OrgRoleDeveloper, store, sqlStore))
		r.Use(d.activity)
		r.Get("/", d.provHandler.GetAuthSettings)
		r.Put("/", d.provHandler.PutAuthSettings)
	})
	r.Route("/api/projects/{projectId}/realtime", func(r chi.Router) {
		r.Use(custommw.TenantContext)
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireProjectAccess(store, sqlStore))
		// Listing the published tables is a read any member may make; enabling
		// or disabling one alters the database publication, so it sits on the
		// same Developer+ rung as every other data-plane authoring surface. The
		// mount enforced membership and nothing else, which let a Viewer call
		// /disable-all (EXC-395).
		r.Use(custommw.RequireProjectRoleForWrites(domain.OrgRoleDeveloper, store, sqlStore))
		r.Use(d.activity)
		d.realtimeHandler.Routes(r)
	})
	// Studio's DocumentDB document browser: reading collections, documents
	// and indexes is open to any member; every write is Developer+, the rung
	// the rest of the data plane uses.
	if d.documentsHandler != nil {
		r.Route("/api/projects/{projectId}/documentdb", func(r chi.Router) {
			r.Use(custommw.TenantContext)
			r.Use(auth.RequireAuth)
			r.Use(custommw.RequireProjectAccess(store, sqlStore))
			r.Use(custommw.RequireProjectRoleForWrites(domain.OrgRoleDeveloper, store, sqlStore))
			r.Use(d.activity)
			d.documentsHandler.Routes(r)
		})
	}
	if d.storageHandler != nil {
		r.Route("/api/projects/{projectId}/storage", func(r chi.Router) {
			r.Use(custommw.TenantContext)
			r.Use(auth.RequireAuth)
			r.Use(custommw.RequireProjectAccess(store, sqlStore))
			// Listing buckets and objects and minting a download URL are reads
			// any member may make; creating or dropping a bucket, uploading,
			// confirming and deleting objects change what the project stores,
			// so they carry the Developer+ gate the rest of the data plane
			// does. The mount enforced membership and nothing else, which let a
			// Viewer delete a bucket and everything in it (EXC-395).
			r.Use(custommw.RequireProjectRoleForWrites(domain.OrgRoleDeveloper, store, sqlStore))
			r.Use(d.activity)
			d.storageHandler.Routes(r)
		})
		// The public object path is anonymous, so it sits outside the
		// project-access gate and carries the rule itself: no downloads are
		// signed for a project the platform must not serve (EXC-401).
		r.Group(func(r chi.Router) {
			r.Use(custommw.RequireServableProject(store))
			d.storageHandler.PublicRoutes(r)
		})
		// Phase 10: ctx.storage runtime-to-provisioning routes. Auth via
		// X-Excalibase-Runtime-Token shared secret — same secret the
		// Deno runtime uses for /deploy and /internal/invoke. Mounted
		// on the root router because internal callers don't have a
		// user JWT and don't ride the project-access middleware.
		d.storageHandler.InternalRoutes(r)
	}
}

// mountEmailRoutes mounts /api/email (verify + reset flows) plus the
// /internal/email/send relay used by excalibase-auth.
func mountEmailRoutes(r *chi.Mux, d *handlerDeps) {
	r.Route("/api/email", func(r chi.Router) {
		// The send is keyed per user, not per project: it mails the caller's
		// own address and names no project to key on.
		d.emailTokensHandler.Routes(r, d.rlMailSend)
	})
	if d.internalEmail == nil {
		return
	}
	// The relay is a service-only route: RequireAuth answers 401 without a
	// valid token, and RequireCapability refuses everything that is not a
	// service token granting email:send — a studio session or an ordinary
	// PAT included, since neither carries a permission list.
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Use(custommw.RequireCapability(custommw.EmailRelayCapability()))
		d.internalEmail.Routes(r)
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
	pgStore, err := pgstore.NewWithMaxConns(cfg.PlatformDBURL, cfg.PlatformDBMaxConns)
	if err != nil {
		log.Fatalf("Failed to init Postgres platform store: %v", err)
	}
	log.Println("Using PostgreSQL platform store")
	return pgStore
}

// buildVault selects the vault backend (remote HTTP, or in-process Shamir on
// the Postgres platform store) and returns the client, the local instance (nil
// for HTTP), and a cleanup function that closes the local vault if any.
func buildVault(cfg config.AppConfig, sqlStore storage.PlatformStore) (vaultclient.VaultClient, *vault.Vault, func()) {
	if cfg.VaultURL != "" {
		log.Printf("Using remote vault at %s", cfg.VaultURL)
		return vaultclient.NewHTTPClient(cfg.VaultURL, cfg.VaultPAT), nil, func() {
			// no-op cleanup: remote HTTP vault has no local handle to close.
		}
	}
	platformStore, ok := sqlStore.(*pgstore.Store)
	if !ok {
		log.Fatal("vault requires the Postgres platform store")
	}
	autoReady := localVaultNeedsAutoReady(cfg)
	localVault, err := newLocalVault(
		vault.NewPostgresStore(platformStore.DB()),
		autoReady,
		cfg.StoragePath+"/unseal.key",
		os.Getenv("VAULT_UNSEAL_KEY"),
	)
	if err != nil {
		log.Fatalf("Failed to init vault (postgres): %v", err)
	}
	log.Printf("Using PostgreSQL vault store (auto-init/unseal at boot: %v)", autoReady)
	return localVault, localVault, func() { localVault.Close() }
}

// logFirstAdminSetupToken establishes the one-time first-admin setup token
// (EXC-451) when the platform has no admin yet.
//
// SETUP_TOKEN, when set, is an operator-supplied token — the platform-aio
// chart's bootstrap Job generates one into a Secret and passes it here so it
// can register the first admin non-interactively; that token is adopted
// (hash stored) but deliberately never logged, since the operator already
// has it. Left unset, a fresh token is generated and printed exactly once,
// here — never persisted in the clear, only its hash, so losing this log
// line means generating a replacement.
func logFirstAdminSetupToken(sqlStore storage.PlatformStore) {
	raw, err := auth.BootstrapSetupToken(context.Background(), sqlStore, os.Getenv("SETUP_TOKEN"))
	if err != nil {
		log.Fatalf("Failed to bootstrap first-admin setup token: %v", err)
	}
	if raw == "" {
		return // admin already exists, or an operator-supplied token was adopted silently
	}
	log.Println("=== FIRST-ADMIN SETUP TOKEN ===")
	log.Printf("First-admin setup token: %s", raw)
	log.Println("Use it once in POST /api/auth/register as \"setupToken\" to create the platform admin.")
	log.Println("================================")
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

// buildDocumentBrowser wires the document browser, which reaches a project's
// gateway as excalibase_app from vault. It needs Kubernetes to find the
// gateway and the cluster CA, so a deployment without one mounts nothing.
func buildDocumentBrowser(k8sClient k8s.KubeClient, vc vaultclient.VaultClient, store storage.InstanceStore) *handler.DocumentBrowserHandler {
	if k8sClient == nil || vc == nil {
		return nil
	}
	connector := docbrowser.NewGatewayConnector(docbrowser.GatewayConnectorConfig{
		Projects: store, Credentials: vc, Cluster: k8sClient,
	})
	return handler.NewDocumentBrowserHandler(docbrowser.NewService(connector, docbrowser.Options{}))
}

// buildDBEndpointService wires a project's public database endpoint (EXC-410):
// the port allocator in the platform database and the per-project
// LoadBalancer Service that publishes it.
//
// It returns nil when the platform offers no public endpoints — no endpoint
// domain configured, a provisioner with no Kubernetes behind it, or a
// platform store that cannot hold the allocator. Callers then carry no
// endpoint step at all, rather than one that is present and fails.
func buildDBEndpointService(cfg config.AppConfig, sqlStore storage.PlatformStore, instances storage.InstanceStore, k8sClient k8s.KubeClient) *service.DBEndpointService {
	if cfg.DBEndpointDomain == "" || cfg.ProvisionerMode != "k8s" {
		return nil
	}
	endpoints, ok := sqlStore.(storage.DatabaseEndpointStore)
	if !ok {
		log.Print("WARN: public database endpoints disabled: the platform store does not hold the port allocator")
		return nil
	}
	return service.NewDBEndpointService(service.DBEndpointServiceConfig{
		Endpoints:    endpoints,
		Instances:    instances,
		Kube:         k8sClient,
		DomainSuffix: cfg.DBEndpointDomain,
		Ports:        cfg.DBEndpointPorts,
		Quarantine:   cfg.DBEndpointPortQuarantine,
		SharedIPKey:  cfg.DBEndpointSharedIPKey,
	})
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
		dockerProvisioner := provisioner.NewDockerPostgreSQLProvisioner(dockerClient)
		dockerProvisioner.SetDeletionPoller(deletionPoller())
		return provisioner.NewFactory(dockerProvisioner), dockerClient
	}
	log.Println("Provisioner mode: k8s (CNPG)")
	pgProvisioner := provisioner.NewPostgreSQLProvisioner(k8sClient, cfg.WatcherChartPath)
	pgProvisioner.SetWatcherImage(cfg.WatcherImage)
	pgProvisioner.SetPublicDomainSuffix(cfg.DBEndpointDomain)
	pgProvisioner.SetDeletionPoller(deletionPoller())
	return provisioner.NewFactory(pgProvisioner), nil
}

// Teardown wait budget. CNPG finalizers plus PVC release routinely take a
// couple of minutes on a busy node.
const (
	deletionPollInterval = 2 * time.Second
	defaultDeletionWait  = 5 * time.Minute
)

// deletionPoller bounds how long a teardown waits for the project's
// resources to actually disappear. Clusters with slow storage detach need a
// longer budget than the default; DELETION_WAIT_TIMEOUT (a Go duration, e.g.
// "10m") raises it. An unparseable value is fatal rather than silently
// falling back — an operator who set it meant it.
func deletionPoller() provisioner.Poller {
	poller := provisioner.NewPoller(deletionPollInterval, defaultDeletionWait)
	raw := os.Getenv("DELETION_WAIT_TIMEOUT")
	if raw == "" {
		return poller
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil || timeout <= 0 {
		log.Fatalf("DELETION_WAIT_TIMEOUT must be a positive Go duration, got %q", raw)
	}
	poller.Timeout = timeout
	return poller
}

// buildPausers extracts the Pauser-implementing provisioners from
// the factory + docker client. Returns an empty map when no pauser
// is wired. PauseService consults this map
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
	provider := cfg.EmailProvider
	if provider == "" {
		provider = "ses" // back-compat default when the operator named none
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
	keyID := cfg.SESAccessKeyID
	secret := cfg.SESSecretAccessKey
	region := cfg.SESRegion
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
		ConfigurationSet: cfg.SESConfigurationSet,
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
	apiKey := cfg.ResendAPIKey
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
	keyID := cfg.R2AccessKeyID
	secret := cfg.R2SecretAccessKey
	endpoint := cfg.R2Endpoint
	bucket := cfg.R2Bucket
	if keyID == "" || secret == "" || endpoint == "" || bucket == "" {
		log.Printf("INFO: R2 not configured, storage feature disabled")
		return nil
	}
	r2, err := storagesvc.NewR2Client(storagesvc.R2Config{
		AccessKeyID:     keyID,
		SecretAccessKey: secret,
		Endpoint:        endpoint,
		Region:          cfg.R2Region,
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
