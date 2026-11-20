package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storagebudget"
)

type budgetSource struct{ err error }

func (b budgetSource) Capacity(context.Context) (int64, error) { return 100 << 30, b.err }
func (budgetSource) StorageAllocated(context.Context) (k8s.StorageAllocation, error) {
	return k8s.StorageAllocation{TenantBytes: 50 << 30, PlatformBytes: 6 << 30}, nil
}

func TestStorageBudgetReport(t *testing.T) {
	h := NewStorageBudgetHandler(storagebudget.New(budgetSource{}, 80))
	w := httptest.NewRecorder()
	h.Report(w, httptest.NewRequest(http.MethodGet, "/api/admin/storage", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var report storagebudget.Report
	if err := json.NewDecoder(w.Body).Decode(&report); err != nil {
		t.Fatal(err)
	}
	if !report.Enabled || report.BudgetBytes != 80<<30 || report.AllocatedBytes != 56<<30 || report.UsedPercent != 70 {
		t.Fatalf("report = %+v", report)
	}
}

// Unmetered installs say so; a storage layer that cannot answer is a 503
// with no internal detail.
func TestStorageBudgetReportUnmeteredAndUnavailable(t *testing.T) {
	w := httptest.NewRecorder()
	NewStorageBudgetHandler(nil).Report(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Fatalf("unmetered: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	NewStorageBudgetHandler(storagebudget.New(budgetSource{err: errors.New("lvmnodes forbidden 10.0.0.1")}, 80)).
		Report(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "10.0.0.1") {
		t.Fatalf("unavailable: %d %s", w.Code, w.Body.String())
	}
}

type hangingSource struct{}

func (hangingSource) Capacity(ctx context.Context) (int64, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}
func (hangingSource) StorageAllocated(context.Context) (k8s.StorageAllocation, error) {
	return k8s.StorageAllocation{}, nil
}

// A storage layer that never answers used to leave Studio's card spinning for
// good; the report now gives up and says so (EXC-757).
func TestStorageBudgetReportGivesUpWhenTheStorageLayerHangs(t *testing.T) {
	previous := storageReadTimeout
	storageReadTimeout = 30 * time.Millisecond
	defer func() { storageReadTimeout = previous }()

	w := httptest.NewRecorder()
	NewStorageBudgetHandler(storagebudget.New(hangingSource{}, 80)).Report(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d %s, want 503", w.Code, w.Body.String())
	}
}
