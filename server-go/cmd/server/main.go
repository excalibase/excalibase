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
	pgstore "github.com/excalibase/provisioning-poc/internal/storage/postgres"
	sqlitestore "github.com/excalibase/provisioning-poc/internal/storage/sqlite"
	"github.com/excalibase/provisioning-poc/pkg/vault"
	"github.com/excalibase/provisioning-poc/internal/vaultclient"
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

	// Platform DB store (Postgres if PLATFORM_DB_URL set, else SQLite)
	var sqlStore storage.PlatformStore
	if cfg.PlatformDBURL != "" {
		pgStore, err := pgstore.New(cfg.PlatformDBURL)
		if err != nil {
			log.Fatalf("Failed to init Postgres: %v", err)
		}
		sqlStore = pgStore
		log.Println("Using PostgreSQL platform store")
	} else {
		sqliteStore, err := sqlitestore.New(cfg.DBPath)
		if err != nil {
			log.Fatalf("Failed to init SQLite: %v", err)
		}
		sqlStore = sqliteStore
		log.Println("Using SQLite platform store")
	}
	defer sqlStore.Close()

	// Use platform store as InstanceStore (same interface)
	var store storage.InstanceStore = sqlStore

	// Keep filesystem store as fallback for parameter groups (until migrated)
	pgStore, _ := storage.NewFileSystemParameterGroupStore(cfg.StoragePath)

	// Vault: remote (VAULT_URL) or local (Postgres/bbolt)
	var vc vaultclient.VaultClient
	var localVault *vault.Vault // only set when using local vault
	if cfg.VaultURL != "" {
		vc = vaultclient.NewHTTPClient(cfg.VaultURL, cfg.VaultPAT)
		log.Printf("Using remote vault at %s", cfg.VaultURL)
	} else {
		if cfg.PlatformDBURL != "" {
			if pgStoreTyped, ok := sqlStore.(*pgstore.Store); ok {
				vaultStore := vault.NewPostgresStore(pgStoreTyped.DB())
				var vErr error
				localVault, vErr = vault.NewWithStore(vaultStore)
				if vErr != nil {
					log.Fatalf("Failed to init vault (postgres): %v", vErr)
				}
				log.Println("Using local PostgreSQL vault store")
			}
		}
		if localVault == nil {
			var vErr error
			vaultPath := cfg.StoragePath + "/vault.bolt"
			localVault, vErr = vault.New(vaultPath)
			if vErr != nil {
				log.Fatalf("Failed to init vault (bbolt): %v", vErr)
			}
			log.Println("Using local bbolt vault store")
		}
		vc = localVault
		defer localVault.Close()
	}

	// Bootstrap admin user on first run
	if err := auth.Bootstrap(context.Background(), sqlStore); err != nil {
		log.Fatalf("Failed to bootstrap admin user: %v", err)
	}

	// Bootstrap default org in self-hosted mode
	if cfg.DeploymentMode == "selfhosted" {
		// Find admin user for org ownership
		users, _ := sqlStore.FindAllUsers(context.Background())
		if len(users) > 0 {
			if err := auth.BootstrapDefaultOrg(context.Background(), sqlStore, users[0].ID); err != nil {
				log.Fatalf("Failed to bootstrap default org: %v", err)
			}
		}
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
	provSvc.SetVault(vc)
	provSvc.SetOrgStore(sqlStore)
	provSvc.SetSelfHostedMode(cfg.DeploymentMode == "selfhosted")

	// PgDog notifier (optional — requires Postgres store + NATS)
	if cfg.PlatformDBURL != "" && cfg.NatsURL != "" {
		if pgStore, ok := sqlStore.(storage.PgDogConfigStore); ok {
			pgdogNotifier, err := service.NewPgDogNotifier(pgStore, cfg.NatsURL)
			if err != nil {
				log.Printf("WARN: pgdog notifier: %v", err)
			} else {
				provSvc.SetPgDogNotifier(pgdogNotifier)
				defer pgdogNotifier.Close()
				log.Println("PgDog notifier enabled (NATS + platform-db)")
			}
		}
	}
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
	// Vault handler only when running local vault (no VAULT_URL)
	var vaultHandler *handler.VaultHandler
	if localVault != nil {
		vaultHandler = handler.NewVaultHandler(localVault)
	}
	schemaHandler := handler.NewSchemaHandler(vc)

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

	// Public config endpoint for frontend
	r.Get("/api/config", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"deploymentMode":"%s"}`, cfg.DeploymentMode)
	})

	// Provisioning API
	r.Route("/api/provision", func(r chi.Router) {
		r.Use(auth.RequireAuth)
		r.Get("/", provHandler.ListInstances)
		r.Post("/", provHandler.Provision)
		r.Post("/estimate", provHandler.EstimateCost)
		r.Post("/byoc", provHandler.ProvisionBYOC)

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

	// Vault API (only when running local vault — remote vault has its own service)
	if vaultHandler != nil {
		r.Route("/api/vault", func(r chi.Router) { vaultHandler.Routes(r) })
	}

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

