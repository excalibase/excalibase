//go:build integration

package appregistry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// zotImage is the chart's appRegistry.image.
const zotImage = "ghcr.io/project-zot/zot-minimal-linux-amd64:v2.1.22@sha256:27729292c45f1dc0a323b69292d15cc13a5ca4b8bf05f81106e7f89a2b1abd08"

func startZot(t *testing.T, issuer *Issuer) string {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	der, err := x509.MarshalPKIXPublicKey(issuer.key.Public())
	if err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(dir, "token.pub")
	if err := os.WriteFile(public, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(dir, "config.json")
	conf := `{"distSpecVersion":"1.1.1","storage":{"rootDirectory":"/tmp/zot"},"http":{"address":"0.0.0.0","port":"5000",
	  "auth":{"bearer":{"realm":"http://token.invalid/internal/registry/token","service":"` + service + `","cert":"/etc/zot/token.pub"}}},"log":{"level":"info"}}`
	if err := os.WriteFile(config, []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: zotImage, ExposedPorts: []string{"5000/tcp"}, Cmd: []string{"serve", "/etc/zot/config.json"},
			Files: []testcontainers.ContainerFile{
				{HostFilePath: config, ContainerFilePath: "/etc/zot/config.json", FileMode: 0o644},
				{HostFilePath: public, ContainerFilePath: "/etc/zot/token.pub", FileMode: 0o644},
			},
			WaitingFor: wait.ForHTTP("/readyz").WithPort("5000/tcp").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start zot: %v", err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "5000/tcp")
	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func send(t *testing.T, method, target, token, contentType string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

// push uploads a one-blob image and answers the status of the last call that did not succeed, or 201.
func push(t *testing.T, base, repository, token string) (string, int) {
	t.Helper()
	return pushAs(t, base, repository, token, "amd64", func(digest string) string { return digest })
}

// pushAs pushes an image of an architecture under the reference reference(digest) names.
func pushAs(t *testing.T, base, repository, token, arch string, reference func(string) string) (string, int) {
	t.Helper()
	config := []byte(`{"architecture":"` + arch + `","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	start := send(t, http.MethodPost, base+"/v2/"+repository+"/blobs/uploads/", token, "", nil)
	if start.StatusCode != http.StatusAccepted {
		return "", start.StatusCode
	}
	location, _ := url.Parse(start.Header.Get("Location"))
	upload := base + location.Path + "?" + location.RawQuery + "&digest=" + digestOf(config)
	if resp := send(t, http.MethodPut, upload, token, "application/octet-stream", config); resp.StatusCode != http.StatusCreated {
		return "", resp.StatusCode
	}
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": digestOf(config), "size": len(config)},
		"layers": []any{}})
	digest := digestOf(manifest)
	resp := send(t, http.MethodPut, base+"/v2/"+repository+"/manifests/"+reference(digest), token, "application/vnd.oci.image.manifest.v1+json", manifest)
	return digest, resp.StatusCode
}

func pullToken(t *testing.T, h *TokenHandler, projectID, password string, scope string) string {
	t.Helper()
	rec := askToken(h, http.MethodGet, projectID, password, url.Values{"service": {service}, "scope": {scope}})
	var body tokenResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("token: %d %s", rec.Code, rec.Body)
	}
	return body.Token
}

// EXC-540: the tokens this package signs are the ones zot takes, and only for what they name.
func TestZot_TakesOurTokensForTheirOwnProjectOnly(t *testing.T) {
	internal, issuer := testInternal(t, fixedGenerations{})
	base := startZot(t, issuer)
	h, err := NewTokenHandler(internal, func(context.Context, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	pushA, _ := issuer.Mint("build-1", []Grant{{Type: "repository", Name: "proj-a/api", Actions: []string{"pull", "push"}}}, 10*time.Minute)
	digest, code := push(t, base, "proj-a/api", pushA)
	if code != http.StatusCreated {
		t.Fatalf("push with the build's token: %d", code)
	}
	if _, code := push(t, base, "proj-b/api", pushA); code != http.StatusUnauthorized {
		t.Errorf("proj-a's push token on proj-b: %d", code)
	}
	if _, code := push(t, base, "proj-a/api", ""); code != http.StatusUnauthorized {
		t.Errorf("anonymous push: %d", code)
	}

	credA, _ := internal.PullCredential("proj-a")
	pullA := pullToken(t, h, credA.Username, credA.Password, "repository:proj-a/api:pull,push")
	manifest := base + "/v2/proj-a/api/manifests/" + digest
	accept := "application/vnd.oci.image.manifest.v1+json"
	if resp := send(t, http.MethodGet, manifest, pullA, accept, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("proj-a pulls its own image: %d", resp.StatusCode)
	}
	if _, code := push(t, base, "proj-a/api", pullA); code != http.StatusUnauthorized {
		t.Errorf("a pull credential's token pushes: %d", code)
	}
	credB, _ := internal.PullCredential("proj-b")
	pullB := pullToken(t, h, credB.Username, credB.Password, "repository:proj-a/api:pull")
	if resp := send(t, http.MethodGet, manifest, pullB, accept, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("proj-b pulls proj-a's image: %d", resp.StatusCode)
	}
	if resp := send(t, http.MethodGet, manifest, "", accept, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("anonymous pull: %d", resp.StatusCode)
	}
	stranger, _ := testIssuer(t)
	forged, _ := stranger.Mint("proj-a", []Grant{{Type: "repository", Name: "proj-a/api", Actions: []string{"pull"}}}, time.Minute)
	if resp := send(t, http.MethodGet, manifest, forged, accept, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("a token signed by another key: %d", resp.StatusCode)
	}
}

// The sweep's client reads the real catalog and tags, and a delete takes the image away.
func TestZot_TheClientListsAndDeletes(t *testing.T) {
	internal, issuer := testInternal(t, fixedGenerations{})
	base := startZot(t, issuer)
	token, _ := issuer.Mint("build", []Grant{{Type: "repository", Name: "proj-a/api", Actions: []string{"pull", "push"}}}, 10*time.Minute)
	tagAt := func(at time.Time) func(string) string {
		return func(digest string) string {
			tag, err := PushTag(digest, at)
			if err != nil {
				t.Fatal(err)
			}
			return tag
		}
	}
	old, code := pushAs(t, base, "proj-a/api", token, "amd64", tagAt(time.Now().Add(-72*time.Hour)))
	if code != http.StatusCreated {
		t.Fatalf("push: %d", code)
	}
	fresh, _ := pushAs(t, base, "proj-a/api", token, "arm64", tagAt(time.Now()))
	client, err := internal.Client(base)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repos, err := client.Repositories(ctx)
	if err != nil || len(repos) != 1 || repos[0] != "proj-a/api" {
		t.Fatalf("catalog %v %v", repos, err)
	}
	tags, err := client.Tags(ctx, "proj-a/api")
	if err != nil || len(tags) != 2 {
		t.Fatalf("tags %v %v", tags, err)
	}
	if err := client.Delete(ctx, "proj-a/api", old); err != nil {
		t.Fatal(err)
	}
	if err := client.Delete(ctx, "proj-a/api", old); err != nil {
		t.Errorf("deleting what is gone: %v", err)
	}
	accept := "application/vnd.oci.image.manifest.v1+json"
	if resp := send(t, http.MethodGet, base+"/v2/proj-a/api/manifests/"+old, token, accept, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a deleted image still pulls: %d", resp.StatusCode)
	}
	if resp := send(t, http.MethodGet, base+"/v2/proj-a/api/manifests/"+fresh, token, accept, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("the image beside it: %d", resp.StatusCode)
	}
	if tags, _ := client.Tags(ctx, "proj-a/api"); len(tags) != 1 {
		t.Errorf("tags after the delete: %v", tags)
	}
}
