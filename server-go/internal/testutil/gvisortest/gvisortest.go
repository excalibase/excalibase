//go:build live

// Package gvisortest fetches the pinned gVisor release, verifies it against
// its published sha512, and hands a k3s test container the runtime and the
// containerd configuration that registers it as the "runsc" handler.
package gvisortest

import (
	"archive/tar"
	"compress/bzip2"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/testcontainers/testcontainers-go"
)

const (
	gvisorRelease    = "20260921.0"
	gvisorReleaseURL = "https://storage.googleapis.com/gvisor/releases/release/" + gvisorRelease + "/x86_64/gvisor.tar.bz2"
	// Handler is the RuntimeClass handler name the containerd config registers.
	Handler = "runsc"
)

// k3s v1.31.6 ships containerd 2.0, which renders config-v3.toml.tmpl on top of its own base config.
const containerdRunscTemplate = `{{ template "base" . }}

[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runsc]
  runtime_type = "io.containerd.runsc.v1"
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.runsc.options]
  TypeUrl = "io.containerd.runsc.v1.options"
  ConfigPath = "/etc/containerd/runsc.toml"
`

// Files are what a k3s container needs to run pods under gVisor on platform (systrap or kvm).
func Files(t testing.TB, platform string) []testcontainers.ContainerFile {
	t.Helper()
	dir := fetchGVisor(t)
	files := []testcontainers.ContainerFile{
		{HostFilePath: filepath.Join(dir, "runsc"), ContainerFilePath: "/bin/runsc", FileMode: 0o755},
		{HostFilePath: filepath.Join(dir, "containerd-shim-runsc-v1"), ContainerFilePath: "/bin/containerd-shim-runsc-v1", FileMode: 0o755},
		{
			Reader:            strings.NewReader(containerdRunscTemplate),
			ContainerFilePath: "/var/lib/rancher/k3s/agent/etc/containerd/config-v3.toml.tmpl",
			FileMode:          0o644,
		},
		{
			Reader:            strings.NewReader(fmt.Sprintf("[runsc_config]\n  platform = %q\n", platform)),
			ContainerFilePath: "/etc/containerd/runsc.toml",
			FileMode:          0o644,
		},
	}
	// runsc refuses to start a sandbox without its sidecar binaries beside it.
	sidecars, err := os.ReadDir(filepath.Join(dir, "gvisor-bin"))
	if err != nil {
		t.Fatalf("read gvisor sidecars: %v", err)
	}
	for _, sidecar := range sidecars {
		files = append(files, testcontainers.ContainerFile{
			HostFilePath:      filepath.Join(dir, "gvisor-bin", sidecar.Name()),
			ContainerFilePath: "/bin/gvisor-bin/" + sidecar.Name(),
			FileMode:          0o755,
		})
	}
	return files
}

// fetchGVisor downloads the pinned official release once, verifies its
// published sha512, and caches the extracted binaries.
func fetchGVisor(t testing.TB) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("user cache dir: %v", err)
	}
	dir := filepath.Join(cache, "excalibase-gvisor", gvisorRelease)
	if _, err := os.Stat(filepath.Join(dir, ".verified")); err == nil {
		return dir
	}
	if err := downloadGVisor(dir); err != nil {
		t.Fatalf("fetch gVisor %s: %v", gvisorRelease, err)
	}
	return dir
}

func downloadGVisor(dir string) error {
	want, err := publishedSHA512()
	if err != nil {
		return err
	}
	archive, err := os.CreateTemp("", "gvisor-*.tar.bz2")
	if err != nil {
		return err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	got, err := downloadHashed(gvisorReleaseURL, archive)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("sha512 mismatch: got %s, published %s", got, want)
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(dir), "staging-")
	if err != nil {
		return err
	}
	if err := extractGVisor(archive, staging); err != nil {
		os.RemoveAll(staging)
		return err
	}
	if err := os.WriteFile(filepath.Join(staging, ".verified"), []byte(want), 0o644); err != nil {
		return err
	}
	return os.Rename(staging, dir)
}

func publishedSHA512() (string, error) {
	var published strings.Builder
	if _, err := downloadHashed(gvisorReleaseURL+".sha512", &published); err != nil {
		return "", err
	}
	fields := strings.Fields(published.String())
	if len(fields) == 0 {
		return "", errors.New("empty sha512 file")
	}
	return fields[0], nil
}

func downloadHashed(url string, into io.Writer) (string, error) {
	response, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", url, response.Status)
	}
	hash := sha512.New()
	if _, err := io.Copy(io.MultiWriter(into, hash), response.Body); err != nil {
		return "", fmt.Errorf("GET %s: %w", url, err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func extractGVisor(archive io.Reader, dir string) error {
	entries := tar.NewReader(bzip2.NewReader(archive))
	for {
		header, err := entries.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if header.Typeflag == tar.TypeReg && wantedGVisorFile(header.Name) {
			if err := writeExecutable(dir, header.Name, entries); err != nil {
				return err
			}
		}
	}
}

func wantedGVisorFile(name string) bool {
	if name == "runsc" || name == "containerd-shim-runsc-v1" {
		return true
	}
	return strings.HasPrefix(name, "gvisor-bin/") && !strings.Contains(name, "..") && strings.Count(name, "/") == 1
}

func writeExecutable(dir, name string, content io.Reader) error {
	root := filepath.Clean(dir)
	path := filepath.Join(root, name)
	if !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return fmt.Errorf("archive entry %q escapes %s", name, root)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, content); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}
