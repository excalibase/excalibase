package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/email"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	sqlitestore "github.com/excalibase/provisioning-poc/internal/storage/sqlite"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	if handleCLIArgs() {
		return
	}
	cfg := config.Load()
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
	case "help", "--help", "-h":
		printHelp()
		return true
	}
	return false
}

// printHelp prints CLI usage information.
func printHelp() {
	fmt.Println("Usage: excalibase-server [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  (none)              Start the HTTP server")
	fmt.Println("  reset-password      Reset admin password (vault-gated)")
	fmt.Println("  recover-instances   Re-discover K8s clusters into SQLite (vault-gated)")
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

	scheduler, schedulerStop := startBackupScheduler(cfg, sqlStore, deps.backupHandler)
	defer schedulerStop()
	if scheduler != nil {
		deps.backupHandler.SetScheduler(scheduler)
	}

	r := buildRouter(cfg, sqlStore, store, deps)

	startServer(cfg, r)
}

// startBackupScheduler launches the cron runtime and replays the
// persistent schedule. Returns the scheduler (so the handler can be
// wired post-construction) and a stop function for graceful shutdown.
// In single-process self-hosted deployments the leader lock is a
// no-op; cloud uses a Postgres advisory lock so multi-replica
// platforms only fire once per tick.
func startBackupScheduler(cfg config.AppConfig, sqlStore storage.PlatformStore, backupHandler *handler.BackupHandler) (*service.BackupScheduler, func()) {
	if backupHandler == nil || sqlStore == nil {
		return nil, func() {}
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
		return nil, func() {}
	}
	return scheduler, scheduler.Stop
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
	capDeps            *capacityDeps
	rlUnauth           func(http.Handler) http.Handler
	rlAuthed           func(http.Handler) http.Handler
	rlDataPlane        func(http.Handler) http.Handler
}

// buildFunctionHandler wires the edge-function handler with stores + runtime client.
func buildFunctionHandler(cfg config.AppConfig, vc vaultclient.VaultClient, store storage.InstanceStore, sqlStore storage.PlatformStore, k8sClient k8s.KubeClient) *handler.FunctionHandler {
	fnStore := edgefn.NewFunctionStore(cfg.StoragePath)
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
	noop := func() {} // notifier never started
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
			Host:      os.Getenv("DOCKER_HOST"),
			CertPath:  os.Getenv("DOCKER_CERT_PATH"),
			TLSVerify: os.Getenv("DOCKER_TLS_VERIFY") != "",
		})
		if err == nil {
			runner := service.NewDockerBackupRunner(dockerSDK.RawClient())
			bucket := envOr("BACKUP_DEFAULT_BUCKET", "excalibase-backups")
			endpoint := envOr("BACKUP_DEFAULT_ENDPOINT", os.Getenv("R2_ENDPOINT"))
			ak := envOr("BACKUP_DEFAULT_ACCESS_KEY_ID", os.Getenv("R2_ACCESS_KEY_ID"))
			sk := envOr("BACKUP_DEFAULT_SECRET_ACCESS_KEY", os.Getenv("R2_SECRET_ACCESS_KEY"))
			region := envOr("BACKUP_DEFAULT_REGION", "auto")
			if ak != "" && sk != "" && endpoint != "" {
				uploader, err := service.NewAWSS3Uploader(context.Background(), service.AWSS3UploaderConfig{
					AccessKeyID:     ak,
					SecretAccessKey: sk,
					Endpoint:        endpoint,
					Region:          region,
					UsePathStyle:    os.Getenv("BACKUP_S3_PATH_STYLE") != "",
				})
				if err == nil {
					adapters[domain.ModeDocker] = service.NewDockerBackupAdapter(service.DockerBackupAdapterConfig{
						Runner:    runner,
						Uploader:  uploader,
						Records:   sqlStore.BackupRecords(),
						Bucket:    bucket,
						KeyPrefix: "backups/",
						Instances: store,
					})
				}
			}
		}
	}

	return service.NewBackupServiceWithAdapters(store, adapters, cfg.StoragePath)
}

// buildHandlerDeps constructs every HTTP handler the router needs.
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

	authHandler := handler.NewAuthHandler(sqlStore, sqlStore)
	authHandler.SetOrgStore(sqlStore)

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
		orgHandler:         handler.NewOrgHandler(sqlStore, sqlStore),
		vaultHandler:       vaultHandler,
		schemaHandler:      handler.NewSchemaHandler(vc),
		realtimeHandler:    realtimeHandler,
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
	mountVaultAndSchemaRoutes(r, d)
	mountProjectScopedRoutes(r, sqlStore, store, d)
	mountEmailRoutes(r, d)

	r.With(custommw.TenantContext).HandleFunc("/functions/v1/{projectId}/{fnId}", d.fnHandler.PublicInvoke)
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
		})
	})
}

// mountSimpleAuthRoutes mounts the small auth-gated subtrees (alerts, setup, parameter groups).
func mountSimpleAuthRoutes(r *chi.Mux, d *handlerDeps) {
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
		d.adminHandler.Routes(r)
	})
}

// mountVaultAndSchemaRoutes mounts the optional vault routes and /api/schema.
func mountVaultAndSchemaRoutes(r *chi.Mux, d *handlerDeps) {
	if d.vaultHandler != nil {
		r.Route("/api/vault", func(r chi.Router) { d.vaultHandler.Routes(r) })
	}
	r.Route("/api/schema", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		d.schemaHandler.Routes(r)
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

// buildPlatformStore selects between Postgres (cloud) and SQLite (self-hosted)
// based on the cloud flag and PLATFORM_DB_URL env.
func buildPlatformStore(cfg config.AppConfig) storage.PlatformStore {
	if cfg.IsCloud() {
		if cfg.PlatformDBURL == "" {
			log.Fatal("cloud mode requires PLATFORM_DB_URL (PostgreSQL connection string)")
		}
		pgStore, err := pgstore.New(cfg.PlatformDBURL)
		if err != nil {
			log.Fatalf("Failed to init Postgres: %v", err)
		}
		log.Println("Cloud mode: using PostgreSQL platform store")
		return pgStore
	}
	sqliteStore, err := sqlitestore.New(cfg.DBPath)
	if err != nil {
		log.Fatalf("Failed to init SQLite: %v", err)
	}
	log.Println("Self-hosted mode: using SQLite platform store")
	return sqliteStore
}

// buildVault selects the vault backend (remote HTTP, Postgres-backed Shamir,
// or bbolt) and returns the client, the local instance (nil for HTTP), and a
// cleanup function that closes the local vault if any.
func buildVault(cfg config.AppConfig, sqlStore storage.PlatformStore) (vaultclient.VaultClient, *vault.Vault, func()) {
	switch {
	case cfg.VaultURL != "":
		log.Printf("Using remote vault at %s", cfg.VaultURL)
		return vaultclient.NewHTTPClient(cfg.VaultURL, cfg.VaultPAT), nil, func() {} // no local handle to close
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
		log.Println("Self-hosted mode: using bbolt vault store")
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
		log.Fatalf("Failed to init K8s client: %v", err)
	}
	return k8sClient
}

// buildProvisionerFactory selects the K8s (CNPG) or Docker provisioner based
// on PROVISIONER_MODE and returns the factory plus the docker client (nil
// when not in docker mode).
func buildProvisionerFactory(cfg config.AppConfig, k8sClient k8s.KubeClient) (*provisioner.Factory, provisioner.DockerClient) {
	if cfg.ProvisionerMode == "docker" {
		dockerClient, err := provisioner.NewRealDockerClient(provisioner.DockerClientOptions{
			Host:      cfg.DockerHost,
			CertPath:  cfg.DockerCertPath,
			TLSVerify: cfg.DockerTLSVerify,
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

// envOr returns os.Getenv(key) or fallback when empty. Local helper to keep
// main.go's per-feature config blocks readable.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// buildEmailSender constructs the SES sender from env. SES_ACCESS_KEY_ID +
// SES_SECRET_ACCESS_KEY are populated from the K8s secret ses-creds via
// the chart. Falls back to a noop sender (returns 503) when env is empty
// so dev/CI runs without AWS access — handlers stay code-complete.
func buildEmailSender(cfg config.AppConfig) email.Sender {
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
	from := os.Getenv("SES_FROM_ADDRESS")
	if from == "" {
		from = cfg.EmailFromAddress
	}
	fromName := os.Getenv("SES_FROM_NAME")
	if fromName == "" {
		fromName = cfg.EmailFromName
	}
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
