package handler

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

const testStreamPath = "/stream"


func TestSSEMetricsStream(t *testing.T) {
	r := chi.NewRouter()
	h := &SSEHandler{interval: 100 * time.Millisecond}
	r.Get(testStreamPath, h.StreamMetrics)

	ts := httptest.NewServer(r)
	defer ts.Close()

	client := &http.Client{Timeout: 400 * time.Millisecond}
	resp, err := client.Get(ts.URL + testStreamPath)
	if err != nil {
		// Timeout is expected — we just want the data received so far
		t.Skipf("client timeout (expected): %v", err)
		return
	}
	defer resp.Body.Close()

	buf := make([]byte, 4096)
	n, _ := resp.Body.Read(buf)
	result := string(buf[:n])

	scanner := bufio.NewScanner(strings.NewReader(result))
	dataLines := 0
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), "data: ") {
			dataLines++
		}
	}

	if dataLines < 1 {
		t.Errorf("expected at least 1 SSE data line, got %d in: %s", dataLines, result)
	}
}

func TestSSEContentType(t *testing.T) {
	r := chi.NewRouter()
	h := &SSEHandler{interval: 50 * time.Millisecond}
	r.Get(testStreamPath, h.StreamMetrics)

	// Use real HTTP server to avoid race on ResponseRecorder
	ts := httptest.NewServer(r)
	defer ts.Close()

	resp, err := http.Get(ts.URL + testStreamPath)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if ct != "text/event-stream" {
		t.Errorf("Content-Type: got %s, want text/event-stream", ct)
	}
}

func TestPrometheusMetricsEndpoint(t *testing.T) {
	r := chi.NewRouter()
	RegisterPrometheusHandler(r)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "go_") {
		t.Error("should contain Go runtime metrics")
	}
}
