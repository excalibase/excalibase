package enginestats

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Answers as Docker 29 and rootless Podman 6 give them (probed 2026-10-29).
const dockerStats = `{"read":"2026-10-28T19:33:02.85648204Z","cpu_stats":{"cpu_usage":{"total_usage":2041244000},"system_cpu_usage":8611402590000000,"online_cpus":24},"precpu_stats":{"cpu_usage":{"total_usage":0}},"memory_stats":{"usage":425984,"stats":{"inactive_file":65536,"active_anon":65536},"limit":66819936256},"networks":{"eth0":{"rx_bytes":1633,"tx_bytes":126},"eth1":{"rx_bytes":7,"tx_bytes":4}}}`

const podmanStats = `{"read":"2026-10-29T02:32:42.87364281+07:00","preread":"0001-01-01T00:00:00Z","cpu_stats":{"cpu_usage":{"total_usage":9675434000},"system_cpu_usage":141775835419000,"online_cpus":24,"cpu":129.48},"precpu_stats":{"cpu_usage":{"total_usage":0}},"memory_stats":{"usage":1433600,"limit":536870912},"name":"exc670probe","networks":{"wlp130s0":{"rx_bytes":456,"tx_bytes":1286}}}`

const stoppedDockerStats = `{"read":"0001-01-01T00:00:00Z","cpu_stats":{"cpu_usage":{"total_usage":0}},"memory_stats":{}}`

func engine(t *testing.T, routes map[string]string) (*Client, *[]string) {
	t.Helper()
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.RequestURI())
		body, ok := routes[r.URL.Path]
		if !ok {
			http.Error(w, `{"message":"No such container"}`, http.StatusNotFound)
			return
		}
		if strings.HasPrefix(body, "500 ") {
			http.Error(w, body[4:], http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	client, err := New("tcp://" + strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return client, &seen
}

func TestNewTakesTheEngineAddressProvisioningIsGiven(t *testing.T) {
	for _, host := range []string{"tcp://engine-proxy:2375", "http://engine-proxy:2375"} {
		if _, err := New(host); err != nil {
			t.Errorf("New(%q): %v", host, err)
		}
	}
	for _, host := range []string{"", "unix:///var/run/docker.sock", "ssh://host", "tcp://"} {
		if _, err := New(host); err == nil {
			t.Errorf("New(%q) accepted: provisioning reaches the engine only through the proxy", host)
		}
	}
}

func TestManagedListsOnlyManagedContainers(t *testing.T) {
	client, seen := engine(t, map[string]string{
		"/containers/json": `[{"Id":"abc","Names":["/excalibase-p1-postgres"],"Image":"postgres:17","State":"running","Status":"Up 3 minutes (unhealthy)","Labels":{"excalibase.managed":"true","excalibase.project":"p1"}},{"Id":"def","Names":["excalibase-p1-watcher"],"State":"exited","Labels":{}}]`,
	})
	containers, err := client.Managed(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(containers) != 2 || containers[0].Name != "excalibase-p1-postgres" || containers[0].ID != "abc" ||
		containers[0].State != "running" || containers[0].Health != "unhealthy" || containers[0].Labels["excalibase.project"] != "p1" {
		t.Fatalf("containers = %+v", containers)
	}
	if containers[1].Name != "excalibase-p1-watcher" || containers[1].State != "exited" || containers[1].Health != "" {
		t.Errorf("second = %+v", containers[1])
	}
	want := `GET /containers/json?all=1&filters=%7B%22label%22%3A%5B%22excalibase.managed%3Dtrue%22%5D%7D`
	if len(*seen) != 1 || (*seen)[0] != want {
		t.Errorf("calls = %v, want %s", *seen, want)
	}
}

func TestInspectReadsStateAndHealth(t *testing.T) {
	client, _ := engine(t, map[string]string{
		"/containers/excalibase-postgres/json": `{"Id":"x1","Name":"/excalibase-postgres","State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}},"Config":{"Image":"postgres:17"}}`,
		"/containers/excalibase-nats/json":     `{"Id":"x2","Name":"/excalibase-nats","State":{"Status":"exited","Running":false}}`,
	})
	got, err := client.Inspect(context.Background(), "excalibase-postgres")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "x1" || got.Name != "excalibase-postgres" || got.State != "running" || got.Health != "healthy" || got.Image != "postgres:17" {
		t.Errorf("inspect = %+v", got)
	}
	got, _ = client.Inspect(context.Background(), "excalibase-nats")
	if got.State != "exited" || got.Health != "" {
		t.Errorf("stopped = %+v", got)
	}
	if _, err := client.Inspect(context.Background(), "excalibase-gone"); err != ErrNotFound {
		t.Errorf("missing container: %v, want ErrNotFound", err)
	}
}

func TestStatsReadsDockerAndPodmanAnswers(t *testing.T) {
	client, seen := engine(t, map[string]string{
		"/containers/docker-one/stats": dockerStats,
		"/containers/podman-one/stats": podmanStats,
	})
	docker, err := client.Stats(context.Background(), "docker-one")
	if err != nil {
		t.Fatal(err)
	}
	if docker.Read.IsZero() || docker.CPUNanos != 2041244000 || docker.OnlineCPUs != 24 {
		t.Errorf("docker cpu = %+v", docker)
	}
	// As docker stats: the page cache the kernel can drop is not usage.
	if docker.MemoryBytes != 425984-65536 || docker.MemoryLimitBytes != 66819936256 {
		t.Errorf("docker memory = %d / %d", docker.MemoryBytes, docker.MemoryLimitBytes)
	}
	if !docker.NetOK || docker.NetRx != 1640 || docker.NetTx != 130 {
		t.Errorf("docker network = %+v", docker)
	}
	podman, err := client.Stats(context.Background(), "podman-one")
	if err != nil {
		t.Fatal(err)
	}
	if podman.CPUNanos != 9675434000 || podman.MemoryBytes != 1433600 || podman.MemoryLimitBytes != 536870912 || podman.NetRx != 456 || podman.NetTx != 1286 {
		t.Errorf("podman = %+v", podman)
	}
	if !strings.HasSuffix((*seen)[0], "/stats?one-shot=true&stream=false") {
		t.Errorf("stats call = %s, want one sample", (*seen)[0])
	}
}

func TestStatsOfAStoppedContainerIsNotRunning(t *testing.T) {
	client, _ := engine(t, map[string]string{
		"/containers/docker-stopped/stats": stoppedDockerStats,
		"/containers/podman-stopped/stats": `500 {"cause":"container is stopped","message":"container is stopped","response":500}`,
	})
	for _, name := range []string{"docker-stopped", "podman-stopped", "gone"} {
		if _, err := client.Stats(context.Background(), name); err == nil {
			t.Errorf("%s: stats of a container that is not running gave a sample", name)
		}
	}
}

func TestAHungEngineIsCutOff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	client, err := New("tcp://" + strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := client.Stats(ctx, "x"); err == nil {
		t.Error("a stats call outlived its context")
	}
}

// frame is one block of the engine's multiplexed log stream.
func frame(stream byte, text string) string {
	size := len(text)
	return string([]byte{stream, 0, 0, 0, byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size)}) + text
}

func TestLogsReadsTheTailOfBothStreams(t *testing.T) {
	client, seen := engine(t, map[string]string{
		"/containers/abc/logs": frame(1, "2026-10-29 LOG:  ready\n") + frame(2, "2026-10-29 FATAL:  password authentication failed\n"),
	})
	out, err := client.Logs(context.Background(), "abc", 50, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if out != "2026-10-29 LOG:  ready\n2026-10-29 FATAL:  password authentication failed\n" {
		t.Errorf("logs = %q", out)
	}
	if got := (*seen)[0]; got != "GET /containers/abc/logs?stderr=1&stdout=1&tail=50" {
		t.Errorf("request = %s", got)
	}
}

func TestLogsSinceAskTheEngineFromThatSecond(t *testing.T) {
	client, seen := engine(t, map[string]string{"/containers/abc/logs": frame(1, "line\n")})
	if _, err := client.Logs(context.Background(), "abc", 10, time.Unix(1761700000, 0)); err != nil {
		t.Fatal(err)
	}
	if got := (*seen)[0]; got != "GET /containers/abc/logs?since=1761700000&stderr=1&stdout=1&tail=10" {
		t.Errorf("request = %s", got)
	}
}

func TestLogsOfATerminalContainerArePlainText(t *testing.T) {
	client, _ := engine(t, map[string]string{"/containers/abc/logs": "plain line one\nplain line two\n"})
	out, err := client.Logs(context.Background(), "abc", 10, time.Time{})
	if err != nil || out != "plain line one\nplain line two\n" {
		t.Errorf("logs = %q %v", out, err)
	}
}

func TestLogsOfAMissingContainerIsNotFound(t *testing.T) {
	client, _ := engine(t, map[string]string{})
	if _, err := client.Logs(context.Background(), "gone", 10, time.Time{}); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestLogsStopAtTheReadBound(t *testing.T) {
	big := strings.Repeat("x", maxAnswer+10)
	client, _ := engine(t, map[string]string{"/containers/abc/logs": frame(1, big)})
	out, err := client.Logs(context.Background(), "abc", 10, time.Time{})
	if err != nil || len(out) > maxAnswer {
		t.Errorf("read %d bytes, %v; want at most %d", len(out), err, maxAnswer)
	}
}
