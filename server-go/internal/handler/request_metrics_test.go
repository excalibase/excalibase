package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/service"
)

type fakeCharts struct {
	project, rangeName string
	err                error
}

func (f *fakeCharts) Charts(_ context.Context, projectID, rangeName string) (*domain.RequestCharts, error) {
	f.project, f.rangeName = projectID, rangeName
	if f.err != nil {
		return nil, f.err
	}
	return &domain.RequestCharts{ProjectID: projectID, Range: rangeName, StepSeconds: 60,
		Points: []domain.RequestChartPoint{}, Totals: []domain.RequestChartPoint{}}, nil
}

func requestChartsRouter(source RequestChartsSource) chi.Router {
	r := chi.NewRouter()
	r.Get("/api/provision/{projectId}/requests", NewRequestMetricsHandler(source).Charts)
	return r
}

func TestRequestCharts_AnswersTheProjectsRange(t *testing.T) {
	source := &fakeCharts{}
	w := httptest.NewRecorder()
	requestChartsRouter(source).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/provision/p1/requests?range=24h", nil))
	if w.Code != http.StatusOK || source.project != "p1" || source.rangeName != "24h" {
		t.Fatalf("got %d, asked %q %q", w.Code, source.project, source.rangeName)
	}
	var body domain.RequestCharts
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Range != "24h" || body.Points == nil {
		t.Errorf("body: %s", w.Body.String())
	}
}

func TestRequestCharts_DefaultsToTheLastHour(t *testing.T) {
	source := &fakeCharts{}
	requestChartsRouter(source).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/provision/p1/requests", nil))
	if source.rangeName != "1h" {
		t.Errorf("default range: %q", source.rangeName)
	}
}

func TestRequestCharts_AnUnknownRangeIsABadRequest(t *testing.T) {
	w := httptest.NewRecorder()
	requestChartsRouter(&fakeCharts{err: service.ErrUnknownChartRange}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/provision/p1/requests?range=30d", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("got %d", w.Code)
	}
}

func TestRequestCharts_AStoreFailureSaysNothingInternal(t *testing.T) {
	w := httptest.NewRecorder()
	requestChartsRouter(&fakeCharts{err: errors.New("pq: relation request_metrics does not exist")}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/provision/p1/requests", nil))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "pq:") || strings.Contains(w.Body.String(), "relation") {
		t.Errorf("got %d %s", w.Code, w.Body.String())
	}
}
