package imagedigest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// fakeRegistry serves one manifest per "repo:tag" over the distribution API,
// behind a bearer token when user is set.
type fakeRegistry struct {
	manifests map[string]string // "repo:reference" -> digest
	user      string
	password  string
	status    int // when set, every manifest request answers it
	heads     atomic.Int32
}

func (f *fakeRegistry) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/token", f.serveToken)
	mux.HandleFunc("/v2/", f.serveManifest)
	return mux
}

func (f *fakeRegistry) serveToken(w http.ResponseWriter, r *http.Request) {
	user, password, ok := r.BasicAuth()
	if !ok || user != f.user || password != f.password {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"token": "granted"})
}

func (f *fakeRegistry) serveManifest(w http.ResponseWriter, r *http.Request) {
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	if f.user != "" && r.Header.Get("Authorization") != "Bearer granted" {
		w.Header().Set("WWW-Authenticate",
			`Bearer realm="http://`+r.Host+`/token",service="fake",scope="repository:x:pull"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	digest, found := f.lookup(r.URL.Path)
	if !found {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.Method == http.MethodHead {
		f.heads.Add(1)
	}
	w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
	w.Header().Set("Docker-Content-Digest", digest)
	w.Header().Set("Content-Length", "2")
	w.WriteHeader(http.StatusOK)
}

// lookup answers a /v2/<repo>/manifests/<tag or digest> path.
func (f *fakeRegistry) lookup(path string) (string, bool) {
	repo, reference, ok := strings.Cut(strings.TrimPrefix(path, "/v2/"), "/manifests/")
	if !ok {
		return "", false
	}
	if digest, found := f.manifests[repo+":"+reference]; found {
		return digest, true
	}
	for _, known := range f.manifests {
		if reference == known {
			return known, true
		}
	}
	return "", false
}

func serve(t *testing.T, registry *fakeRegistry) (string, *Resolver) {
	t.Helper()
	server := httptest.NewServer(registry.handler())
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")
	return host, newResolver(server.Client(), true)
}

func TestResolveATagAnonymously(t *testing.T) {
	registry := &fakeRegistry{manifests: map[string]string{"acme/web:main": testDigest}}
	host, resolver := serve(t, registry)

	got, err := resolver.Resolve(context.Background(), host+"/acme/web:main", nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != testDigest {
		t.Fatalf("digest = %q, want %q", got, testDigest)
	}
	if registry.heads.Load() != 1 {
		t.Fatalf("a tag is resolved with one HEAD, which no registry counts as a pull; got %d", registry.heads.Load())
	}
}

func TestResolveADigestConfirmsItExists(t *testing.T) {
	registry := &fakeRegistry{manifests: map[string]string{"acme/web:main": testDigest}}
	host, resolver := serve(t, registry)

	got, err := resolver.Resolve(context.Background(), host+"/acme/web@"+testDigest, nil)
	if err != nil || got != testDigest {
		t.Fatalf("Resolve by digest = %q, %v", got, err)
	}
	other := "sha256:" + strings.Repeat("f", 64)
	if _, err := resolver.Resolve(context.Background(), host+"/acme/web@"+other, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown digest: %v, want ErrNotFound", err)
	}
}

func TestResolveWithTheSavedCredential(t *testing.T) {
	registry := &fakeRegistry{manifests: map[string]string{"acme/private:v1": testDigest}, user: "ci", password: "s3cret"}
	host, resolver := serve(t, registry)

	if _, err := resolver.Resolve(context.Background(), host+"/acme/private:v1", nil); !errors.Is(err, ErrDenied) {
		t.Fatalf("anonymous against a private repository: %v, want ErrDenied", err)
	}
	wrong := &apphost.RegistryCredential{Username: "ci", Password: "wrong"}
	if _, err := resolver.Resolve(context.Background(), host+"/acme/private:v1", wrong); !errors.Is(err, ErrDenied) {
		t.Fatalf("a wrong password: %v, want ErrDenied", err)
	}
	right := &apphost.RegistryCredential{Username: "ci", Password: "s3cret"}
	got, err := resolver.Resolve(context.Background(), host+"/acme/private:v1", right)
	if err != nil || got != testDigest {
		t.Fatalf("with the saved credential = %q, %v", got, err)
	}
}

func TestResolveClassifiesRegistryRefusals(t *testing.T) {
	for status, want := range map[int]error{
		http.StatusNotFound:           ErrNotFound,
		http.StatusTooManyRequests:    ErrRateLimited,
		http.StatusForbidden:          ErrDenied,
		http.StatusServiceUnavailable: ErrUnavailable,
	} {
		registry := &fakeRegistry{status: status}
		host, resolver := serve(t, registry)
		_, err := resolver.Resolve(context.Background(), host+"/acme/web:main", nil)
		if !errors.Is(err, want) {
			t.Errorf("status %d: %v, want %v", status, err, want)
		}
	}
}

func TestResolveRefusesAnInvalidReference(t *testing.T) {
	_, resolver := serve(t, &fakeRegistry{})
	for _, bad := range []string{"", "nginx", "UPPER/case:1", "ghcr.io/acme/web:bad tag"} {
		if _, err := resolver.Resolve(context.Background(), bad, nil); !errors.Is(err, apphost.ErrInvalidImage) {
			t.Errorf("Resolve(%q): %v, want ErrInvalidImage", bad, err)
		}
	}
}

// The control plane dials a registry the caller named, so it must never be a
// door into the cluster's own network or the cloud metadata service.
func TestResolveNeverDialsAnInternalAddress(t *testing.T) {
	registry := &fakeRegistry{manifests: map[string]string{"acme/web:main": testDigest}}
	server := httptest.NewServer(registry.handler())
	t.Cleanup(server.Close)
	host := strings.TrimPrefix(server.URL, "http://")

	resolver := NewResolver()
	resolver.plainHTTP = true
	for _, image := range []string{host + "/acme/web:main", "169.254.169.254/latest/meta:data", "localhost:5000/web:1"} {
		_, err := resolver.Resolve(context.Background(), image, nil)
		if !errors.Is(err, ErrNotPublic) {
			t.Errorf("Resolve(%q): %v, want ErrNotPublic", image, err)
		}
	}
}

func TestRepositoryLocation(t *testing.T) {
	for image, want := range map[string]string{
		"nginx:1.27":                                    "registry-1.docker.io/library/nginx",
		"docker.io/acme/web:1":                          "registry-1.docker.io/acme/web",
		"index.docker.io/library/a:1":                   "registry-1.docker.io/library/a",
		"ghcr.io/acme/web:main":                         "ghcr.io/acme/web",
		"registry.example.com:8443/a/b/c@" + testDigest: "registry.example.com:8443/a/b/c",
	} {
		got, _ := repositoryLocation(image)
		if got != want {
			t.Errorf("repositoryLocation(%q) = %q, want %q", image, got, want)
		}
	}
}
