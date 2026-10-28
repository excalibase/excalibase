package sitestest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"
)

// Entry is one header of a test archive; Body is a regular file's content.
type Entry struct {
	Header tar.Header
	Body   string
}

func File(name, body string) Entry {
	return Entry{Header: tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}, Body: body}
}

func Dir(name string) Entry {
	return Entry{Header: tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}}
}

// Tar writes the entries as an uncompressed tar archive.
func Tar(t testing.TB, entries ...Entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := tar.NewWriter(&buf)
	for _, entry := range entries {
		header := entry.Header
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatalf("write header %s: %v", header.Name, err)
		}
		if entry.Body != "" {
			if _, err := writer.Write([]byte(entry.Body)); err != nil {
				t.Fatalf("write body %s: %v", header.Name, err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

func Gzip(t testing.TB, raw []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write(raw); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// Dist is a Vite-shaped build output as `tar -czf dist.tgz -C dist .` writes it.
func Dist(t testing.TB, index string) []byte {
	t.Helper()
	return Gzip(t, Tar(t,
		Dir("./"),
		File("./index.html", index),
		Dir("./assets/"),
		File("./assets/index-abc.js", "console.log(1)"),
		File("./assets/index-abc.css", "body{}"),
	))
}

// ServerSiteJSON is the site.json of an OpenNext build (EXC-661).
const ServerSiteJSON = `{"kind":"server","main":"worker.js","compatibilityDate":"2025-09-01","compatibilityFlags":["nodejs_compat"],"framework":"nextjs"}`

// ServerDist is a server deploy as the upload layout packs it: worker is the main module's code.
func ServerDist(t testing.TB, worker string) []byte {
	t.Helper()
	return Gzip(t, Tar(t,
		Dir("./"),
		File("./site.json", ServerSiteJSON),
		Dir("./server/"),
		File("./server/worker.js", worker),
		File("./server/worker.js.map", `{"version":3}`),
		Dir("./assets/"),
		File("./assets/BUILD_ID", "build-1"),
		File("./assets/_next/static/chunk.js", "console.log(1)"),
	))
}
