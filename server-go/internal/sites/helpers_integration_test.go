//go:build integration

package sites_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
)

// acceptingPublisher is a CDN that already holds every file.
type acceptingPublisher struct{}

func (acceptingPublisher) StartUploadSession(context.Context, string, map[string]cfworkers.ManifestEntry) (cfworkers.UploadSession, error) {
	return cfworkers.UploadSession{JWT: "completion"}, nil
}

func (acceptingPublisher) UploadBucket(context.Context, string, []cfworkers.Asset) (string, error) {
	return "completion", nil
}

func (acceptingPublisher) PutScript(context.Context, string, string, cfworkers.AssetConfig) error {
	return nil
}

func (acceptingPublisher) DeleteScript(context.Context, string) error { return nil }

func sampleArchive(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	zipped := gzip.NewWriter(&raw)
	archive := tar.NewWriter(zipped)
	for name, body := range map[string]string{"./index.html": "<div id=app></div>", "./assets/app.js": "1"} {
		if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatalf("header: %v", err)
		}
		if _, err := archive.Write([]byte(body)); err != nil {
			t.Fatalf("body: %v", err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("tar: %v", err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	return raw.Bytes()
}
