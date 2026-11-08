package imagepull

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

func TestDrain_ReturnsTheErrorAPullStreamCarries(t *testing.T) {
	stream := `{"status":"Pulling from library/postgres"}` + "\n" +
		`{"errorDetail":{"message":"unexpected EOF"},"error":"unexpected EOF"}` + "\n"
	err := Drain(strings.NewReader(stream))
	if err == nil || !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("a stream that ended in an error must fail, got %v", err)
	}
}

func TestDrain_AcceptsACompletePull(t *testing.T) {
	stream := `{"status":"Pulling from library/postgres"}` + "\n" + `{"status":"Status: Downloaded newer image"}` + "\n"
	if err := Drain(strings.NewReader(stream)); err != nil {
		t.Fatalf("a complete pull must pass: %v", err)
	}
}

type fakeEngine struct {
	present  bool
	pulls    []string
	streams  []string
	pullErrs []error
}

func (f *fakeEngine) ImageInspect(_ context.Context, _ string, _ ...client.ImageInspectOption) (image.InspectResponse, error) {
	if f.present {
		return image.InspectResponse{}, nil
	}
	return image.InspectResponse{}, errNotFound{}
}

func (f *fakeEngine) ImagePull(_ context.Context, ref string, _ image.PullOptions) (io.ReadCloser, error) {
	attempt := len(f.pulls)
	f.pulls = append(f.pulls, ref)
	if attempt < len(f.pullErrs) && f.pullErrs[attempt] != nil {
		return nil, f.pullErrs[attempt]
	}
	body := `{"status":"ok"}`
	if attempt < len(f.streams) {
		body = f.streams[attempt]
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

type errNotFound struct{}

func (errNotFound) Error() string { return "No such image" }
func (errNotFound) NotFound()     {}

func TestEnsure_SkipsAPresentImage(t *testing.T) {
	engine := &fakeEngine{present: true}
	if err := Ensure(context.Background(), engine, "postgres:16", 3, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if len(engine.pulls) != 0 {
		t.Fatalf("pulled an image the engine holds: %v", engine.pulls)
	}
}

func TestEnsure_RetriesAPullThatFailedInTheStream(t *testing.T) {
	engine := &fakeEngine{streams: []string{`{"error":"net/http: TLS handshake timeout"}`, `{"status":"ok"}`}}
	if err := Ensure(context.Background(), engine, "postgres:16", 3, time.Millisecond); err != nil {
		t.Fatalf("the second attempt succeeded: %v", err)
	}
	if len(engine.pulls) != 2 {
		t.Fatalf("pulls = %d, want 2", len(engine.pulls))
	}
}

func TestEnsure_GivesUpAfterTheAttempts(t *testing.T) {
	engine := &fakeEngine{pullErrs: []error{errors.New("connection reset"), errors.New("connection reset"), errors.New("connection reset")}}
	err := Ensure(context.Background(), engine, "postgres:16", 3, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("want the last pull error, got %v", err)
	}
	if len(engine.pulls) != 3 {
		t.Fatalf("pulls = %d, want 3", len(engine.pulls))
	}
}

func TestDrain_ReadsTheErrorDetailToo(t *testing.T) {
	err := Drain(strings.NewReader(`{"errorDetail":{"message":"blob unknown"}}` + "\n"))
	if err == nil || !strings.Contains(err.Error(), "blob unknown") {
		t.Fatalf("errorDetail alone must fail, got %v", err)
	}
}

func TestEnsure_DoesNotRetryARefusalThatWillNotChange(t *testing.T) {
	engine := &fakeEngine{pullErrs: []error{fmt.Errorf("denied: %w", errdefs.ErrUnauthenticated), nil}}
	if err := Ensure(context.Background(), engine, "ghcr.io/x/y:1", 3, time.Millisecond); err == nil {
		t.Fatal("an unauthorized pull must fail")
	}
	if len(engine.pulls) != 1 {
		t.Fatalf("pulls = %d, want 1", len(engine.pulls))
	}
}

func TestEnsureLocal_PullsThroughTheDaemonTheEnvironmentNames(t *testing.T) {
	ryukOnce = sync.Once{}
	t.Setenv("HOME", t.TempDir())
	var pulled []string
	mux := http.NewServeMux()
	mux.HandleFunc("/_ping", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/json"):
			http.Error(w, `{"message":"No such image"}`, http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/images/create"):
			pulled = append(pulled, r.URL.Query().Get("fromImage"))
			_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
		default:
			http.NotFound(w, r)
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(server.URL, "http://"))
	if err := EnsureLocal(context.Background(), "postgres:16"); err != nil {
		t.Fatal(err)
	}
	if len(pulled) != 2 || !strings.Contains(pulled[0], "ryuk") || !strings.Contains(pulled[1], "postgres") {
		t.Fatalf("pulled %v, want the reaper image then the requested one", pulled)
	}
}

func TestEnsureLocal_LeavesAMissingDaemonToTheCaller(t *testing.T) {
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:1")
	if err := EnsureLocal(context.Background(), "postgres:16"); err != nil {
		t.Fatalf("no daemon is not this package's failure: %v", err)
	}
}

func TestEnsure_FailsOnceOnARefusalRetryingCannotChange(t *testing.T) {
	cases := map[string]*fakeEngine{
		"http 429":                    {pullErrs: []error{fmt.Errorf("denied: %w", errdefs.ErrResourceExhausted)}},
		"500 toomanyrequests":         {pullErrs: []error{errors.New("Error response from daemon: toomanyrequests: retry-after: 1ms, allowed: 44000/minute")}},
		"in-stream toomanyrequests":   {streams: []string{`{"error":"toomanyrequests: rate exceeded"}`}},
		"in-stream manifest unknown":  {streams: []string{`{"error":"manifest unknown"}`}},
		"in-stream too many requests": {streams: []string{`{"errorDetail":{"message":"Too Many Requests"}}`}},
	}
	for name, engine := range cases {
		err := Ensure(context.Background(), engine, "ghcr.io/x/y:1", 3, time.Millisecond)
		if err == nil {
			t.Errorf("%s: want a failure", name)
		}
		if len(engine.pulls) != 1 {
			t.Errorf("%s: pulls = %d, want 1 (a refusal is not retried)", name, len(engine.pulls))
		}
		if err != nil && !strings.Contains(err.Error(), "registry refused") {
			t.Errorf("%s: error should say the registry refused it: %v", name, err)
		}
	}
}

func TestDockerHost_FollowsTestcontainersOrder(t *testing.T) {
	// testcontainers v0.42.0 core.extractDockerHost: tc.host, DOCKER_HOST,
	// the default socket when it exists, then docker.host.
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".testcontainers.properties"), []byte("# c\ntc.host=tcp://tc:1\ndocker.host=unix:///custom/docker.sock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	onlyDockerHost := t.TempDir()
	if err := os.WriteFile(filepath.Join(onlyDockerHost, ".testcontainers.properties"), []byte("docker.host=unix:///custom/docker.sock\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	socketThere := func(string) bool { return true }
	socketGone := func(string) bool { return false }
	cases := []struct {
		name   string
		env    map[string]string
		home   string
		socket func(string) bool
		want   string
	}{
		{"tc.host beats DOCKER_HOST", map[string]string{"DOCKER_HOST": "tcp://h:2375"}, home, socketThere, "tcp://tc:1"},
		{"DOCKER_HOST beats the socket", map[string]string{"DOCKER_HOST": "tcp://h:2375"}, onlyDockerHost, socketThere, "tcp://h:2375"},
		{"the default socket beats docker.host", nil, onlyDockerHost, socketThere, ""},
		{"docker.host when there is no socket", nil, onlyDockerHost, socketGone, "unix:///custom/docker.sock"},
		{"nothing configured", nil, t.TempDir(), socketGone, ""},
	}
	for _, tc := range cases {
		if got := dockerHost(env(tc.env), tc.home, tc.socket); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPermanent_ALocalPermissionErrorIsNotARegistryRefusal(t *testing.T) {
	local := errors.New("Cannot connect: dial unix /var/run/docker.sock: connect: permission denied")
	if permanent(local) {
		t.Error("a local socket permission error must not be reported as a registry refusal")
	}
	if !permanent(errors.New("pull access denied for x, repository does not exist")) {
		t.Error("a registry denial is a refusal")
	}
}

func TestRyukDisabled_ReadsEnvironmentAndPropertiesLikeTestcontainers(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".testcontainers.properties"), []byte("ryuk.disabled=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	cases := []struct {
		name string
		env  map[string]string
		home string
		want bool
	}{
		{"env true", map[string]string{"TESTCONTAINERS_RYUK_DISABLED": "true"}, t.TempDir(), true},
		{"properties true", nil, home, true},
		{"env false overrides properties true", map[string]string{"TESTCONTAINERS_RYUK_DISABLED": "false"}, home, false},
		// v0.42.0 config.read sets RyukDisabled = (value == "true") for any boolean value,
		// so "1" does not disable the reaper there and it still has to be pulled.
		{"env 1 does not disable it", map[string]string{"TESTCONTAINERS_RYUK_DISABLED": "1"}, t.TempDir(), false},
		{"nothing set", nil, t.TempDir(), false},
	}
	for _, tc := range cases {
		if got := ryukDisabled(env(tc.env), tc.home); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
