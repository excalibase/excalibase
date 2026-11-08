package provisioner

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubDaemon answers the few calls ensureImage makes; the pull's stream body
// is whatever the test hands it.
func stubDaemon(t *testing.T, pullStream string) *RealDockerClient {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/images/json"):
			_, _ = w.Write([]byte("[]"))
		case strings.HasSuffix(r.URL.Path, "/images/create"):
			_, _ = w.Write([]byte(pullStream))
		default:
			http.NotFound(w, r)
		}
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	docker, err := NewRealDockerClient(DockerClientOptions{Host: "tcp://" + strings.TrimPrefix(server.URL, "http://")})
	if err != nil {
		t.Fatal(err)
	}
	return docker
}

func TestEnsureImage_FailsWhenThePullFailsInsideTheStream(t *testing.T) {
	docker := stubDaemon(t, `{"status":"Pulling from library/postgres"}`+"\n"+`{"errorDetail":{"message":"toomanyrequests"},"error":"toomanyrequests"}`+"\n")
	err := docker.ensureImage(context.Background(), "postgres:16")
	if err == nil || !strings.Contains(err.Error(), "toomanyrequests") {
		t.Fatalf("a pull that failed in the stream must fail, got %v", err)
	}
}

func TestEnsureImage_AcceptsACompletePull(t *testing.T) {
	docker := stubDaemon(t, `{"status":"Status: Downloaded newer image"}`+"\n")
	if err := docker.ensureImage(context.Background(), "postgres:16"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureImage_ReportsACancelledContextNotSuccess(t *testing.T) {
	docker := stubDaemon(t, `{"status":"Pulling"}`+"\n"+`{"error":"context canceled"}`+"\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := docker.ensureImage(ctx, "postgres:16"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled pull must report the cancellation, got %v", err)
	}
}
