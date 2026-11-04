package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/logdrain"
)

// EXC-658: the forwarder's routes answer only on the internal listener, so
// the edge cannot reach them whatever token it is handed.
func TestLogForwarderRoutesAnswerOnTheInternalListenerOnly(t *testing.T) {
	router, _ := darkFeatureRouter(t, features.LogDrains)
	public, internal := newListeners(config.AppConfig{}, router)
	pull := func(handler http.Handler, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, logdrain.PullPath, strings.NewReader(`{"max":1}`))
		req.Header.Set(logdrain.TokenHeader, token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	// The edge learns nothing, not even whether a token is right.
	for _, token := range []string{"matrix-forwarder-token", "wrong"} {
		if rec := pull(public.Handler, token); rec.Code != http.StatusNotFound {
			t.Errorf("public listener, token %s: %d %s", token, rec.Code, rec.Body)
		}
	}
	if rec := pull(internal.Handler, "matrix-forwarder-token"); rec.Code != http.StatusOK {
		t.Errorf("internal listener: %d %s", rec.Code, rec.Body)
	}
}

// EXC-658 on a single host: drains read the Loki hostlogs fills, with the
// shipper's labels; Kubernetes keeps reading the chart's Loki.
func TestDrainLogStore(t *testing.T) {
	singleHost := config.AppConfig{ProvisionerMode: "docker", LokiURL: "http://ignored:3100",
		DarkFeatures: []features.Feature{features.LogDrains, features.HostLogs}, HostLogs: config.HostLogs{LokiURL: "http://loki:3100"}}
	if url, onHost := drainLogStore(singleHost); url != "http://loki:3100" || !onHost {
		t.Errorf("single host: %q %v", url, onHost)
	}
	noHostLogs := singleHost
	noHostLogs.DarkFeatures = []features.Feature{features.LogDrains}
	if url, onHost := drainLogStore(noHostLogs); url != "" || onHost {
		t.Errorf("single host without hostlogs: %q %v", url, onHost)
	}
	unset := singleHost
	unset.HostLogs = config.HostLogs{}
	if url, onHost := drainLogStore(unset); url != "" || onHost {
		t.Errorf("single host without a Loki URL: %q %v", url, onHost)
	}
	k8s := config.AppConfig{LokiURL: "http://loki.platform:3100", DarkFeatures: []features.Feature{features.LogDrains}}
	if url, onHost := drainLogStore(k8s); url != "http://loki.platform:3100" || onHost {
		t.Errorf("kubernetes: %q %v", url, onHost)
	}
}
