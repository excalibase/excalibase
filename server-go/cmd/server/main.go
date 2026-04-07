package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/excalibase/provisioning-poc/internal/auth"
	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	sqlitestore "github.com/excalibase/provisioning-poc/internal/storage/sqlite"
	"github.com/excalibase/provisioning-poc/internal/vault"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	// CLI subcommands (vault-gated recovery tools)
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "reset-password":
			resetPasswordCLI()
			return
		case "recover-instances":
			recoverInstancesCLI()
			return
		case "help", "--help", "-h":
			fmt.Println("Usage: excalibase-server [command]")
			fmt.Println()
			fmt.Println("Commands:")
			fmt.Println("  (none)              Start the HTTP server")
			fmt.Println("  reset-password      Reset admin password (vault-gated)")
			fmt.Println("  recover-instances   Re-discover K8s clusters into SQLite (vault-gated)")
			fmt.Println("  help                Show this help")
			return
		}
	}

	cfg := config.Load()

	// SQLite storage
	sqlStore, err := sqlitestore.New(cfg.DBPath)
	if err != nil {
		log.Fatalf("Failed to init SQLite: %v", err)
	}
	defer sqlStore.Close()

	// Use SQLite store as InstanceStore (same interface)
	var store storage.InstanceStore = sqlStore

	// Keep filesystem store as fallback for parameter groups (until migrated)
	pgStore, _ := storage.NewFileSystemParameterGroupStore(cfg.StoragePath)

	// Vault (bbolt)
	vaultPath := cfg.StoragePath + "/vault.bolt"
	v, err := vault.New(vaultPath)
	if err != nil {
		log.Fatalf("Failed to init vault: %v", err)
	}
	defer v.Close()

	// Bootstrap admin user on first run
	if err := auth.Bootstrap(context.Background(), sqlStore); err != nil {
		log.Fatalf("Failed to bootstrap admin user: %v", err)
	}

	// Kubernetes client
	k8sClient, err := k8s.NewClient()
	if err != nil {
		log.Fatalf("Failed to init K8s client: %v", err)
	}

	// Provisioners
	pgProvisioner := provisioner.NewPostgreSQLProvisioner(k8sClient, cfg.WatcherChartPath)
	factory := provisioner.NewFactory(pgProvisioner)

	// Edge Functions
	fnStore := edgefn.NewScriptStore(cfg.StoragePath)
	fnClient := edgefn.NewRuntimeClient(cfg.DenoRuntimeURL, cfg.DenoRuntimeSecret)
	hookSvc := edgefn.NewHookService(fnStore, fnClient)
	fnHandler := handler.NewEdgeFnHandler(fnStore, fnClient)

	// Services
	provSvc := service.NewProvisioningService(store, factory, k8sClient)
	provSvc.SetHookService(hookSvc)
	provSvc.SetVault(v)
	metricsSvc := service.NewMetricsService(store, k8sClient, cfg.StoragePath)
	backupSvc := service.NewBackupService(store, k8sClient, cfg.StoragePath)
	perfSvc := service.NewPerformanceService(store, k8sClient)
	auditSvc := service.NewAuditService(store, k8sClient)
	snapshotSvc := service.NewSnapshotService(store, k8sClient, cfg.StoragePath)
	migrationSvc := service.NewMigrationService(store, k8sClient, cfg.StoragePath)
	alertSvc := service.NewAlertingService(cfg.StoragePath)
	setupSvc := service.NewOperatorSetupService(k8sClient)

	// Handlers
	provHandler := handler.NewProvisioningHandler(provSvc, sqlStore)
	metricsHandler := handler.NewMetricsHandler(metricsSvc)
	backupHandler := handler.NewBackupHandler(backupSvc)
	perfHandler := handler.NewPerformanceHandler(perfSvc)
	auditHandler := handler.NewAuditHandler(auditSvc)
	snapshotHandler := handler.NewSnapshotHandler(snapshotSvc)
	migrationHandler := handler.NewMigrationHandler(migrationSvc)
	alertHandler := handler.NewAlertHandler(alertSvc)
	setupHandler := handler.NewSetupHandler(setupSvc)
	pgHandler := handler.NewParameterGroupHandler(pgStore)

	// Auth handler
	authHandler := handler.NewAuthHandler(sqlStore, sqlStore)
	authHandler.SetOrgStore(sqlStore)

	// Org handler
	orgHandler := handler.NewOrgHandler(sqlStore, sqlStore)

	// Vault + Schema handlers
	vaultHandler := handler.NewVaultHandler(v)
	schemaHandler := handler.NewSchemaHandler(v)

	// Router
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(custommw.SecurityHeaders)
	r.Use(custommw.CORS(cfg.CORSOrigins))
	r.Use(auth.ExtractAuth(sqlStore))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// Provisioning API
	r.Route("/api/provision", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Get("/", provHandler.ListInstances)
		r.Post("/", provHandler.Provision)
		r.Post("/estimate", provHandler.EstimateCost)

		r.Route("/{projectId}", func(r chi.Router) {
			r.Get("/", provHandler.GetStatus)
			r.Delete("/", provHandler.Delete)
			r.Get("/credentials", provHandler.GetCredentials)
			r.Patch("/deletion-protection", provHandler.SetDeletionProtection)
			r.Get("/logs", provHandler.GetLogs)
			r.Post("/credentials/rotate", provHandler.RotateCredentials)
			r.Put("/maintenance-window", provHandler.SetMaintenanceWindow)
			r.Get("/maintenance-window", provHandler.GetMaintenanceWindow)

			r.Route("/metrics", func(r chi.Router) { metricsHandler.Routes(r) })
			r.Route("/backup", func(r chi.Router) { backupHandler.Routes(r) })
			r.Route("/performance", func(r chi.Router) { perfHandler.Routes(r) })
			r.Route("/audit", func(r chi.Router) { auditHandler.Routes(r) })
			r.Route("/snapshot", func(r chi.Router) { snapshotHandler.Routes(r) })
			r.Route("/migrations", func(r chi.Router) { migrationHandler.Routes(r) })
		})
	})

	// Alerts API
	r.Route("/api/alerts", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		alertHandler.Routes(r)
	})

	// Setup API
	r.Route("/api/setup", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		setupHandler.Routes(r)
	})

	// Parameter Groups API
	r.Route("/api/parameter-groups", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		pgHandler.Routes(r)
	})

	// Auth API (register + login are public, rest requires auth)
	r.Route("/api/auth", func(r chi.Router) {
		r.Post("/register", authHandler.Register)
		r.Post("/login", authHandler.Login)
		r.With(auth.RequireAuth).Get("/me", authHandler.Me)
		r.With(auth.RequireAuth).Route("/users", func(r chi.Router) {
			r.With(auth.RequirePermission(auth.PermManageUsers)).Get("/", authHandler.ListUsers)
			r.With(auth.RequirePermission(auth.PermManageUsers)).Post("/", authHandler.CreateUser)
			r.With(auth.RequirePermission(auth.PermManageUsers)).Delete("/{userId}", authHandler.DeleteUser)
		})
		r.With(auth.RequireAuth).Route("/tokens", func(r chi.Router) {
			r.Get("/", authHandler.ListTokens)
			r.Post("/", authHandler.CreateToken)
			r.Delete("/{tokenHash}", authHandler.RevokeToken)
		})
	})

	// Org API (requires auth — org-level permissions checked in handler)
	r.Route("/api/orgs", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		orgHandler.Routes(r)
	})

	// Vault API (init/unseal are public, secrets require auth)
	r.Route("/api/vault", func(r chi.Router) { vaultHandler.Routes(r) })

	// Schema API (requires auth + unsealed vault)
	r.Route("/api/schema", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		schemaHandler.Routes(r)
	})

	// Edge Functions API (auth required, manage_functions permission for mutations)
	r.Route("/api/functions", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.With(auth.RequirePermission(auth.PermViewAny)).Get("/", fnHandler.List)
		r.With(auth.RequirePermission(auth.PermViewAny)).Get("/runtime/status", fnHandler.RuntimeStatus)
		r.With(auth.RequirePermission(auth.PermManageFunctions)).Post("/", fnHandler.Create)
		r.Route("/{fnId}", func(r chi.Router) {
			r.With(auth.RequirePermission(auth.PermViewAny)).Get("/", fnHandler.Get)
			r.With(auth.RequirePermission(auth.PermManageFunctions)).Delete("/", fnHandler.Delete)
			r.With(auth.RequirePermission(auth.PermManageFunctions)).Post("/invoke", fnHandler.Invoke)
		})
	})

	addr := fmt.Sprintf(":%s", cfg.Port)
	log.Printf("Excalibase Go server starting on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

