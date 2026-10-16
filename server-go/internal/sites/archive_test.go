package sites_test

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/excalibase/provisioning-poc/internal/testutil/sitestest"
)

func readTestArchive(t *testing.T, raw []byte, limits sites.Limits) (*sites.Bundle, error) {
	t.Helper()
	bundle, err := sites.ReadArchive(bytes.NewReader(raw), limits, t.TempDir())
	if bundle != nil {
		t.Cleanup(func() { _ = bundle.Close() })
	}
	return bundle, err
}

func wantArchiveError(t *testing.T, err error, fragment string) {
	t.Helper()
	var archiveErr *sites.ArchiveError
	if !errors.As(err, &archiveErr) {
		t.Fatalf("err = %v, want an sites.ArchiveError", err)
	}
	if !strings.Contains(archiveErr.Error(), fragment) {
		t.Fatalf("err = %q, want it to mention %q", archiveErr.Error(), fragment)
	}
}

func TestReadArchiveAcceptsAViteDistAsTarGz(t *testing.T) {
	raw := sitestest.Gzip(t, sitestest.Tar(t,
		sitestest.Entry{Header: tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}},
		sitestest.File("./index.html", "<!doctype html><div id=app></div>"),
		sitestest.Entry{Header: tar.Header{Name: "./assets/", Typeflag: tar.TypeDir, Mode: 0o755}},
		sitestest.File("./assets/index-abc123.js", "console.log('hi')"),
		sitestest.File("./assets/index-abc123.css", "body{margin:0}"),
	))
	bundle, err := readTestArchive(t, raw, sites.DefaultLimits)
	if err != nil {
		t.Fatalf("sites.ReadArchive: %v", err)
	}
	if len(bundle.Files) != 3 {
		t.Fatalf("files = %d, want 3", len(bundle.Files))
	}
	manifest := bundle.Manifest()
	entry, ok := manifest["/assets/index-abc123.js"]
	if !ok {
		t.Fatalf("manifest misses /assets/index-abc123.js: %v", manifest)
	}
	if entry.Size != int64(len("console.log('hi')")) {
		t.Errorf("size = %d", entry.Size)
	}
	if bundle.TotalBytes != int64(len("<!doctype html><div id=app></div>")+len("console.log('hi')")+len("body{margin:0}")) {
		t.Errorf("total bytes = %d", bundle.TotalBytes)
	}
}

func TestReadArchiveAcceptsAPlainTar(t *testing.T) {
	bundle, err := readTestArchive(t, sitestest.Tar(t, sitestest.File("index.html", "hi")), sites.DefaultLimits)
	if err != nil {
		t.Fatalf("sites.ReadArchive: %v", err)
	}
	if bundle.Files[0].Path != "/index.html" {
		t.Errorf("path = %q, want /index.html", bundle.Files[0].Path)
	}
}

// Cloudflare takes any 32 hex characters; the content's SHA-256 keeps a hash stable across deploys.
func TestReadArchiveHashesTheContent(t *testing.T) {
	content := "body{margin:0}"
	bundle, err := readTestArchive(t, sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.File("style.css", content)), sites.DefaultLimits)
	if err != nil {
		t.Fatalf("sites.ReadArchive: %v", err)
	}
	sum := sha256.Sum256([]byte(content))
	want := hex.EncodeToString(sum[:])[:32]
	if got := bundle.Manifest()["/style.css"].Hash; got != want {
		t.Fatalf("hash = %q, want %q", got, want)
	}
}

func TestReadArchiveKeepsContentAndType(t *testing.T) {
	bundle, err := readTestArchive(t, sitestest.Tar(t, sitestest.File("index.html", "<p>hello</p>"), sitestest.File("app.js", "1")), sites.DefaultLimits)
	if err != nil {
		t.Fatalf("sites.ReadArchive: %v", err)
	}
	for _, file := range bundle.Files {
		if file.Path != "/index.html" {
			continue
		}
		if !strings.HasPrefix(file.ContentType, "text/html") {
			t.Errorf("content type = %q", file.ContentType)
		}
		reader, err := bundle.Open(file)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		body, _ := io.ReadAll(reader)
		_ = reader.Close()
		if string(body) != "<p>hello</p>" {
			t.Errorf("content = %q", body)
		}
	}
}

func TestReadArchiveRefusals(t *testing.T) {
	small := sites.Limits{MaxFiles: 3, MaxFileBytes: 10, MaxTotalBytes: 15}
	cases := []struct {
		name     string
		raw      []byte
		limits   sites.Limits
		fragment string
	}{
		{"absolute path", sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.File("/etc/passwd", "x")), sites.DefaultLimits, "absolute"},
		{"parent segment", sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.File("assets/../../x.js", "x")), sites.DefaultLimits, ".."},
		{"backslash", sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.File(`assets\x.js`, "x")), sites.DefaultLimits, "backslash"},
		{"symlink", sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.Entry{Header: tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}), sites.DefaultLimits, "symbolic link"},
		{"hardlink", sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.Entry{Header: tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "index.html"}}), sites.DefaultLimits, "hard link"},
		{"char device", sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.Entry{Header: tar.Header{Name: "dev", Typeflag: tar.TypeChar}}), sites.DefaultLimits, "device"},
		{"block device", sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.Entry{Header: tar.Header{Name: "dev", Typeflag: tar.TypeBlock}}), sites.DefaultLimits, "device"},
		{"fifo", sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.Entry{Header: tar.Header{Name: "pipe", Typeflag: tar.TypeFifo}}), sites.DefaultLimits, "pipe"},
		{"duplicate", sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.File("./index.html", "y")), sites.DefaultLimits, "twice"},
		{"no root index", sitestest.Tar(t, sitestest.File("dist/index.html", "x")), sites.DefaultLimits, "index.html"},
		{"empty", sitestest.Tar(t), sites.DefaultLimits, "index.html"},
		{"too many entries", sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.Dir("a/"), sitestest.Dir("b/"), sitestest.Dir("c/"), sitestest.Dir("d/"), sitestest.Dir("e/"), sitestest.Dir("f/")), small, "entries"},
		{"too many files", sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.File("a", "x"), sitestest.File("b", "x"), sitestest.File("c", "x")), small, "files"},
		{"file too large", sitestest.Tar(t, sitestest.File("index.html", "01234567890")), small, "larger"},
		{"total too large", sitestest.Tar(t, sitestest.File("index.html", "0123456789"), sitestest.File("b", "0123456789")), small, "total"},
		{"not an archive", []byte("this is not a tar archive at all, it is plain text that goes on long enough"), sites.DefaultLimits, "tar"},
		{"control character", sitestest.Tar(t, sitestest.File("index.html", "x"), sitestest.File("a\nb.js", "x")), sites.DefaultLimits, "control"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readTestArchive(t, tc.raw, tc.limits)
			wantArchiveError(t, err, tc.fragment)
		})
	}
}

func TestReadArchiveMarksSizeRefusalsAsTooLarge(t *testing.T) {
	small := sites.Limits{MaxFiles: 3, MaxFileBytes: 10, MaxTotalBytes: 15}
	_, err := readTestArchive(t, sitestest.Tar(t, sitestest.File("index.html", "0123456789"), sitestest.File("b", "0123456789")), small)
	var archiveErr *sites.ArchiveError
	if !errors.As(err, &archiveErr) || !archiveErr.TooLarge {
		t.Fatalf("err = %v, want a too-large sites.ArchiveError", err)
	}
}

func TestReadArchiveRemovesItsSpoolOnRefusal(t *testing.T) {
	spool := t.TempDir()
	_, err := sites.ReadArchive(bytes.NewReader(sitestest.Tar(t, sitestest.File("a.js", "x"))), sites.DefaultLimits, spool)
	if err == nil {
		t.Fatal("want a refusal: no index.html")
	}
	entries, readErr := readDirNames(spool)
	if readErr != nil {
		t.Fatalf("read spool: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("spool keeps %v after a refusal", entries)
	}
}

func readDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names, nil
}

// git archive and some tar builds start with a pax global header; it carries no file.
func TestReadArchiveSkipsAPaxGlobalHeader(t *testing.T) {
	global := sitestest.Entry{Header: tar.Header{Name: "pax_global_header", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"comment": "abc"}}}
	bundle, err := readTestArchive(t, sitestest.Tar(t, global, sitestest.File("index.html", "x")), sites.DefaultLimits)
	if err != nil {
		t.Fatalf("ReadArchive: %v", err)
	}
	if len(bundle.Files) != 1 {
		t.Fatalf("files = %+v", bundle.Files)
	}
}

// A body cut off by the caller's size cap keeps its cause, so it can be answered as too large.
func TestReadArchiveKeepsTheReadFailureCause(t *testing.T) {
	cause := errors.New("body too large")
	raw := sitestest.Tar(t, sitestest.File("index.html", strings.Repeat("x", 4096)))
	_, err := sites.ReadArchive(io.MultiReader(bytes.NewReader(raw[:1024]), failingReader{cause}), sites.DefaultLimits, t.TempDir())
	if !errors.Is(err, cause) {
		t.Fatalf("err = %v, want the read failure kept", err)
	}
}

type failingReader struct{ err error }

func (f failingReader) Read([]byte) (int, error) { return 0, f.err }
