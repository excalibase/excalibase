package provisioner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// volumeAPI answers volume calls as the engine does and remembers them.
func volumeAPI(t *testing.T, created map[string]map[string]string, removed *[]string) *RealDockerClient {
	t.Helper()
	version := regexp.MustCompile(`^/v[0-9.]+`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := version.ReplaceAllString(r.URL.Path, "")
		switch {
		case path == "/_ping":
			w.Header().Set("Api-Version", "1.41")
			_, _ = w.Write([]byte("OK"))
		case r.Method == http.MethodPost && path == "/volumes/create":
			var body struct {
				Name   string
				Labels map[string]string
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.Name == "refused" {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"disk full"}`))
				return
			}
			created[body.Name] = body.Labels
			_ = json.NewEncoder(w).Encode(map[string]string{"Name": body.Name})
		case r.Method == http.MethodDelete && path == "/volumes/busy":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"volume is in use"}`))
		case r.Method == http.MethodDelete && path == "/volumes/gone":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"no such volume"}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/volumes/"):
			*removed = append(*removed, strings.TrimPrefix(path, "/volumes/"))
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	c, err := NewRealDockerClient(DockerClientOptions{Host: "tcp://" + strings.TrimPrefix(srv.URL, "http://")})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return c
}

// EXC-592: a project's data volume is created under the managed label, and
// removing one already gone is not an error.
func TestRealClientCreatesAndRemovesManagedVolumes(t *testing.T) {
	created := map[string]map[string]string{}
	var removed []string
	c := volumeAPI(t, created, &removed)
	if err := c.CreateVolume(context.Background(), "excalibase-vol-p1-mysql"); err != nil {
		t.Fatal(err)
	}
	if created["excalibase-vol-p1-mysql"]["excalibase.managed"] != "true" {
		t.Fatalf("created %v", created)
	}
	if err := c.RemoveVolume(context.Background(), "excalibase-vol-p1-mysql"); err != nil || len(removed) != 1 {
		t.Fatalf("remove: %v %v", err, removed)
	}
	if err := c.RemoveVolume(context.Background(), "gone"); err != nil {
		t.Fatalf("a missing volume: %v", err)
	}
	if c.CreateVolume(context.Background(), "refused") == nil || c.RemoveVolume(context.Background(), "busy") == nil {
		t.Fatal("an engine error was swallowed")
	}
}

// An engine client that cannot hold a volume refuses the project rather than
// put its data in the container.
func TestDockerMySQLRefusesAnEngineWithoutVolumes(t *testing.T) {
	mysql := NewDockerMySQLProvisioner(newMockDocker())
	if _, err := mysql.StartOnVolume(context.Background(), "p1", "mysql:8.4.10", testTier); err == nil {
		t.Fatal("started without a data volume")
	}
	if err := mysql.RemoveData(context.Background(), "p1"); err == nil {
		t.Fatal("removed data it cannot hold")
	}
}
