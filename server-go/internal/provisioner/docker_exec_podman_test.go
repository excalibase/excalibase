package provisioner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

// podmanExecAPI answers exec calls the way Podman's Docker-compatible API
// does: an attached start of an exec created without any stream is refused.
func podmanExecAPI(t *testing.T) *httptest.Server {
	t.Helper()
	attached := false
	version := regexp.MustCompile(`^/v[0-9.]+`)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := version.ReplaceAllString(r.URL.Path, "")
		switch {
		case path == "/_ping":
			w.Header().Set("Api-Version", "1.41")
			_, _ = w.Write([]byte("OK"))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/exec"):
			var body struct{ AttachStdout, AttachStderr, AttachStdin bool }
			_ = json.NewDecoder(r.Body).Decode(&body)
			attached = body.AttachStdout || body.AttachStderr || body.AttachStdin
			_ = json.NewEncoder(w).Encode(map[string]string{"Id": "e1"})
		case path == "/exec/e1/start":
			var body struct{ Detach bool }
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !body.Detach && !attached {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"message":"must provide at least one stream to attach to: invalid argument"}`))
				return
			}
			w.WriteHeader(http.StatusOK)
		case path == "/exec/e1/json":
			_ = json.NewEncoder(w).Encode(map[string]any{"Running": false, "ExitCode": 3})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestExecInContainerRunsOnPodman(t *testing.T) {
	srv := podmanExecAPI(t)
	defer srv.Close()
	c, err := NewRealDockerClient(DockerClientOptions{Host: "tcp://" + strings.TrimPrefix(srv.URL, "http://")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	code, err := c.ExecInContainer(context.Background(), "c1", []string{"pg_isready"})
	if err != nil {
		t.Fatalf("exec refused: %v", err)
	}
	if code != 3 {
		t.Fatalf("exit code = %d, want 3", code)
	}
}

// Podman 4.9 reports an empty Health block for a container with no
// healthcheck; running is then all there is to wait for.
func TestWaitForHealthyAcceptsPodmansEmptyHealth(t *testing.T) {
	version := regexp.MustCompile(`^/v[0-9.]+`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path := version.ReplaceAllString(r.URL.Path, ""); path {
		case "/_ping":
			w.Header().Set("Api-Version", "1.41")
			_, _ = w.Write([]byte("OK"))
		case "/containers/c1/json":
			_, _ = w.Write([]byte(`{"Id":"c1","State":{"Status":"running","Running":true,"Health":{"Status":"","FailingStreak":0,"Log":null}},"Config":{"Healthcheck":null}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c, err := NewRealDockerClient(DockerClientOptions{Host: "tcp://" + strings.TrimPrefix(srv.URL, "http://")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := c.WaitForHealthy(ctx, "c1"); err != nil {
		t.Fatalf("running container without a healthcheck not accepted: %v", err)
	}
}
