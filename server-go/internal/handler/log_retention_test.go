package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/service"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"github.com/go-chi/chi/v5"
)

type noNamespacePlans struct{}

func (noNamespacePlans) ProjectNamespacePlans(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (noNamespacePlans) SetNamespacePlan(context.Context, string, string) error { return nil }

func logsRouter(t *testing.T, flags features.Flags) chi.Router {
	t.Helper()
	store, _ := storage.NewFileSystemStore(t.TempDir())
	mock := k8s.NewMockClient()
	seedInstance(store, mock)
	mock.PodLogs["org1-test-db/test-db-postgres-1"] = "log line 1\n"
	svc := service.NewProvisioningService(store, provisioner.NewFactory(), mock)
	svc.SetLogRetention(flags, config.LogRetention{
		Default: 168 * time.Hour,
		ByPlan:  map[domain.TierType]time.Duration{domain.Free: 24 * time.Hour, domain.Standard: 72 * time.Hour},
	}, noNamespacePlans{})
	r := chi.NewRouter()
	r.Get("/api/provision/{projectId}/logs", NewProvisioningHandler(svc, nil).GetLogs)
	return r
}

// EXC-659: Studio says how long the project's logs are kept, from its plan.
func TestGetLogs_SaysHowLongThePlanKeepsLogs(t *testing.T) {
	w := httptest.NewRecorder()
	logsRouter(t, features.NewStatic(features.LogRetention)).ServeHTTP(w, httptest.NewRequest("GET", "/api/provision/test-db/logs?since=7d", nil))
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if w.Code != 200 || resp["retentionHours"] != float64(72) || resp["logs"] != "log line 1\n" {
		t.Fatalf("got %d %v", w.Code, resp)
	}
}

func TestGetLogs_SaysNothingOfRetentionWithTheFlagOff(t *testing.T) {
	w := httptest.NewRecorder()
	logsRouter(t, features.NewStatic()).ServeHTTP(w, httptest.NewRequest("GET", "/api/provision/test-db/logs", nil))
	var resp map[string]any
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if _, ok := resp["retentionHours"]; ok || w.Code != 200 {
		t.Fatalf("flag off: %d %v", w.Code, resp)
	}
}
