package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/edgefn"
	"github.com/excalibase/provisioning-poc/internal/handler"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

func main() {
	cfg := config.Load()

	// Storage
	store, err := storage.NewFileSystemStore(cfg.StoragePath)
	if err != nil {
		log.Fatalf("Failed to init storage: %v", err)
	}
	pgStore, _ := storage.NewFileSystemParameterGroupStore(cfg.StoragePath)

	// Kubernetes client
	k8sClient, err := k8s.NewClient()
	if err != nil {
		log.Fatalf("Failed to init K8s client: %v", err)
	}

	// Provisioners
	pgProvisioner := provisioner.NewPostgreSQLProvisioner(k8sClient)
	factory := provisioner.NewFactory(pgProvisioner)

	// Edge Functions
	fnStore := edgefn.NewScriptStore(cfg.StoragePath)
	fnClient := edgefn.NewRuntimeClient(cfg.DenoRuntimeURL)
	hookSvc := edgefn.NewHookService(fnStore, fnClient)
	fnHandler := handler.NewEdgeFnHandler(fnStore, fnClient)

	// Services
	provSvc := service.NewProvisioningService(store, factory)
	provSvc.SetHookService(hookSvc)
	metricsSvc := service.NewMetricsService(store, k8sClient, cfg.StoragePath)
	backupSvc := service.NewBackupService(store, k8sClient, cfg.StoragePath)
	perfSvc := service.NewPerformanceService(store, k8sClient)
	auditSvc := service.NewAuditService(store, k8sClient)
	snapshotSvc := service.NewSnapshotService(store, k8sClient, cfg.StoragePath)
	migrationSvc := service.NewMigrationService(store, k8sClient, cfg.StoragePath)
	alertSvc := service.NewAlertingService(cfg.StoragePath)
	setupSvc := service.NewOperatorSetupService()

	// Handlers
	provHandler := handler.NewProvisioningHandler(provSvc)
	metricsHandler := handler.NewMetricsHandler(metricsSvc)
	backupHandler := handler.NewBackupHandler(backupSvc)
	perfHandler := handler.NewPerformanceHandler(perfSvc)
	auditHandler := handler.NewAuditHandler(auditSvc)
	snapshotHandler := handler.NewSnapshotHandler(snapshotSvc)
	migrationHandler := handler.NewMigrationHandler(migrationSvc)
	alertHandler := handler.NewAlertHandler(alertSvc)
	setupHandler := handler.NewSetupHandler(setupSvc)
	pgHandler := handler.NewParameterGroupHandler(pgStore)

	// Router
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: true,
		MaxAge:           3600,
	}))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	// Provisioning API
	r.Route("/api/provision", func(r chi.Router) {
		r.Get("/", provHandler.ListInstances)
		r.Post("/", provHandler.Provision)
		r.Post("/estimate", provHandler.EstimateCost)

		r.Route("/{projectId}", func(r chi.Router) {
			r.Get("/", provHandler.GetStatus)
			r.Delete("/", provHandler.Delete)
			r.Get("/credentials", provHandler.GetCredentials)
			r.Patch("/deletion-protection", provHandler.SetDeletionProtection)
			r.Get("/logs", logsHandler(provSvc, k8sClient))
			r.Post("/credentials/rotate", rotateHandler(provSvc, k8sClient))
			r.Put("/maintenance-window", maintenanceSetHandler(provSvc))
			r.Get("/maintenance-window", maintenanceGetHandler(provSvc))

			r.Route("/metrics", func(r chi.Router) { metricsHandler.Routes(r) })
			r.Route("/backup", func(r chi.Router) { backupHandler.Routes(r) })
			r.Route("/performance", func(r chi.Router) { perfHandler.Routes(r) })
			r.Route("/audit", func(r chi.Router) { auditHandler.Routes(r) })
			r.Route("/snapshot", func(r chi.Router) { snapshotHandler.Routes(r) })
			r.Route("/migrations", func(r chi.Router) { migrationHandler.Routes(r) })
		})
	})

	// Alerts API
	r.Route("/api/alerts", func(r chi.Router) { alertHandler.Routes(r) })

	// Setup API
	r.Route("/api/setup", func(r chi.Router) { setupHandler.Routes(r) })

	// Parameter Groups API
	r.Route("/api/parameter-groups", func(r chi.Router) { pgHandler.Routes(r) })

	// Edge Functions API
	r.Route("/api/functions", func(r chi.Router) { fnHandler.Routes(r) })

	addr := fmt.Sprintf(":%s", cfg.Port)
	log.Printf("Excalibase Go server starting on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func logsHandler(svc *service.ProvisioningService, k *k8s.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectId")
		lines := 100
		if l := r.URL.Query().Get("lines"); l != "" {
			if v, err := strconv.Atoi(l); err == nil { lines = v }
		}
		out, err := svc.GetLogs(r.Context(), projectID, lines, k)
		if err != nil {
			w.WriteHeader(500)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"logs": out})
	}
}

func rotateHandler(svc *service.ProvisioningService, k *k8s.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectId")
		creds, err := svc.RotateCredentials(r.Context(), projectID, k)
		if err != nil {
			w.WriteHeader(500)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(creds)
	}
}

func maintenanceSetHandler(svc *service.ProvisioningService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectId")
		var cfg domain.MaintenanceWindowConfig
		json.NewDecoder(r.Body).Decode(&cfg)
		if err := svc.SetMaintenanceWindow(projectID, cfg); err != nil {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
	}
}

func maintenanceGetHandler(svc *service.ProvisioningService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID := chi.URLParam(r, "projectId")
		cfg, err := svc.GetMaintenanceWindow(projectID)
		if err != nil {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cfg)
	}
}
