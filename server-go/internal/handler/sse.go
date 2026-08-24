package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// SSEHandler streams Server-Sent Events at a configurable interval.
type SSEHandler struct {
	interval time.Duration
}

func NewSSEHandler(interval time.Duration) *SSEHandler {
	return &SSEHandler{interval: interval}
}

func (h *SSEHandler) StreamMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ticker := time.NewTicker(h.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			event := map[string]interface{}{
				"timestamp": time.Now().Format(time.RFC3339),
				"type":      "metrics",
			}
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// RegisterPrometheusHandler mounts /metrics for Prometheus scraping.
func RegisterPrometheusHandler(r chi.Router) {
	r.Handle("/metrics", promhttp.Handler())
}
