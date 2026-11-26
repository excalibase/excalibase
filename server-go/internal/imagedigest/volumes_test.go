package imagedigest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// blobRegistry serves an image index, one manifest and its config over the distribution API.
type blobRegistry struct {
	blobs     map[string][]byte // digest -> body
	mediaType map[string]string
	tags      map[string]string // "repo:tag" -> digest
}

func newBlobRegistry() *blobRegistry {
	return &blobRegistry{blobs: map[string][]byte{}, mediaType: map[string]string{}, tags: map[string]string{}}
}

func (b *blobRegistry) add(mediaType string, body []byte) (digest string, size int) {
	sum := sha256.Sum256(body)
	digest = "sha256:" + hex.EncodeToString(sum[:])
	b.blobs[digest], b.mediaType[digest] = body, mediaType
	return digest, len(body)
}

func (b *blobRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v2/")
	var reference string
	if repo, ref, ok := strings.Cut(rest, "/manifests/"); ok {
		reference = ref
		if digest, tagged := b.tags[repo+":"+ref]; tagged {
			reference = digest
		}
	} else if _, ref, ok := strings.Cut(rest, "/blobs/"); ok {
		reference = ref
	}
	body, found := b.blobs[reference]
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", b.mediaType[reference])
	w.Header().Set("Docker-Content-Digest", reference)
	_, _ = w.Write(body)
}

func imageWithVolumes(t *testing.T, registry *blobRegistry, index bool, volumes ...string) {
	t.Helper()
	declared := map[string]struct{}{}
	for _, path := range volumes {
		declared[path] = struct{}{}
	}
	config, _ := json.Marshal(map[string]any{"architecture": "amd64", "os": "linux", "config": map[string]any{"Volumes": declared}})
	configDigest, configSize := registry.add("application/vnd.oci.image.config.v1+json", config)
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": configDigest, "size": configSize},
		"layers": []any{}})
	manifestDigest, manifestSize := registry.add("application/vnd.oci.image.manifest.v1+json", manifest)
	if !index {
		registry.tags["acme/web:v1"] = manifestDigest
		return
	}
	list, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": []any{
		map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": "sha256:" + strings.Repeat("a", 64), "size": 10,
			"platform": map[string]string{"os": "unknown", "architecture": "unknown"}},
		map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": manifestDigest, "size": manifestSize,
			"platform": map[string]string{"os": "linux", "architecture": "amd64"}},
	}})
	indexDigest, _ := registry.add("application/vnd.oci.image.index.v1+json", list)
	registry.tags["acme/web:v1"] = indexDigest
}

func volumesOf(t *testing.T, registry *blobRegistry) ([]string, error) {
	t.Helper()
	server := httptest.NewServer(registry)
	t.Cleanup(server.Close)
	resolver := newResolver(server.Client(), true)
	return resolver.Volumes(context.Background(), strings.TrimPrefix(server.URL, "http://")+"/acme/web:v1", nil)
}

func TestVolumesReadsWhatTheImageDeclares(t *testing.T) {
	for name, index := range map[string]bool{"a plain manifest": false, "an index": true} {
		registry := newBlobRegistry()
		imageWithVolumes(t, registry, index, "/var/lib/app", "/cache")
		got, err := volumesOf(t, registry)
		if err != nil || !slices.Equal(got, []string{"/cache", "/var/lib/app"}) {
			t.Errorf("%s: volumes %v (%v), want /cache and /var/lib/app", name, got, err)
		}
	}
}

func TestVolumesOfAnImageWithoutAnyIsEmpty(t *testing.T) {
	registry := newBlobRegistry()
	imageWithVolumes(t, registry, false)
	if got, err := volumesOf(t, registry); err != nil || len(got) != 0 {
		t.Fatalf("volumes %v (%v), want none", got, err)
	}
}

func TestVolumesRefusesWhatItCannotRead(t *testing.T) {
	if _, err := volumesOf(t, newBlobRegistry()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown image: %v, want ErrNotFound", err)
	}
}

func TestVolumesRefusesAnOversizedConfig(t *testing.T) {
	registry := newBlobRegistry()
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "digest": "sha256:" + strings.Repeat("b", 64), "size": 1 << 30},
		"layers": []any{}})
	digest, _ := registry.add("application/vnd.oci.image.manifest.v1+json", manifest)
	registry.tags["acme/web:v1"] = digest
	if _, err := volumesOf(t, registry); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a 1 GiB config: %v, want ErrUnavailable", err)
	}
}
