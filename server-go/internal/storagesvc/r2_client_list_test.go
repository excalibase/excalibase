package storagesvc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// An S3 stub is the only way to exercise the request/response halves of the
// listing and delete paths: the real endpoint is unreachable from tests and
// an unreachable one can only ever produce failures.
func newStubbedR2(t *testing.T, handler http.HandlerFunc) *R2Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewR2Client(R2Config{
		AccessKeyID: "k", SecretAccessKey: "s",
		Endpoint: srv.URL, Bucket: testPlatformBucket,
	})
	if err != nil {
		t.Fatalf("NewR2Client: %v", err)
	}
	return c
}

func TestR2_ListObjects_ReturnsKeysUnderBucketPrefix(t *testing.T) {
	var gotPrefix string
	c := newStubbedR2(t, func(w http.ResponseWriter, r *http.Request) {
		gotPrefix = r.URL.Query().Get("prefix")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Name>` + testPlatformBucket + `</Name>
  <Contents><Key>projects/proj-abc/buckets/assets/a.txt</Key><Size>3</Size></Contents>
  <Contents><Key>projects/proj-abc/buckets/assets/b.txt</Key><Size>4</Size></Contents>
</ListBucketResult>`))
	})

	objects, err := c.ListObjects(context.Background(), testProjABC, "assets", 10)
	if err != nil {
		t.Fatalf("ListObjectKeys: %v", err)
	}
	if len(objects) != 2 {
		t.Fatalf("unexpected objects: %v", objects)
	}
	// Keys come back relative to the bucket, matching the catalogue's view.
	if objects[0].Key != "a.txt" && objects[1].Key != "a.txt" {
		t.Errorf("unexpected keys: %v", objects)
	}
	if want := "projects/proj-abc/buckets/assets/"; gotPrefix != want {
		t.Errorf("listing prefix: got %q, want %q", gotPrefix, want)
	}
}

// limit 0 means "as many as the backend will give"; the request must still
// carry a valid max-keys rather than asking for zero.
func TestR2_ListObjects_DefaultsLimit(t *testing.T) {
	var gotMaxKeys string
	c := newStubbedR2(t, func(w http.ResponseWriter, r *http.Request) {
		gotMaxKeys = r.URL.Query().Get("max-keys")
		_, _ = w.Write([]byte(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></ListBucketResult>`))
	})

	objects, err := c.ListObjects(context.Background(), testProjABC, "assets", 0)
	if err != nil {
		t.Fatalf("ListObjectKeys: %v", err)
	}
	if len(objects) != 0 {
		t.Errorf("empty listing should yield no keys, got %v", objects)
	}
	if gotMaxKeys == "" || gotMaxKeys == "0" {
		t.Errorf("max-keys should default to a positive value, got %q", gotMaxKeys)
	}
}

func TestR2_ListObjectsStub_ReportsBackendError(t *testing.T) {
	c := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
	})
	if _, err := c.ListObjects(context.Background(), testProjABC, "assets", 5); err == nil {
		t.Fatal("a refused listing must not read as an empty bucket")
	}
}

// A key the backend no longer has is a completed delete, so a retry of a
// half-finished cascade converges instead of failing forever.
func TestR2_DeleteObject_MissingKeyIsSuccess(t *testing.T) {
	c := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`))
	})
	if err := c.DeleteObject(context.Background(), testProjABC, "assets", "gone.txt"); err != nil {
		t.Fatalf("deleting an absent key should succeed: %v", err)
	}
}

func TestR2_DeleteObject_ReportsBackendError(t *testing.T) {
	c := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code><Message>denied</Message></Error>`))
	})
	err := c.DeleteObject(context.Background(), testProjABC, "assets", "a.txt")
	if err == nil || !strings.Contains(err.Error(), "delete object") {
		t.Fatalf("a refused delete must surface, got %v", err)
	}
}

func TestR2_DeleteObject_Succeeds(t *testing.T) {
	var gotPath string
	c := newStubbedR2(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.DeleteObject(context.Background(), testProjABC, "assets", "a.txt"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if !strings.HasSuffix(gotPath, "projects/proj-abc/buckets/assets/a.txt") {
		t.Errorf("delete hit the wrong key: %s", gotPath)
	}
}

func TestR2_HeadObject_ReturnsSizeAndType(t *testing.T) {
	c := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "42")
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("ETag", `"abc123"`)
		w.WriteHeader(http.StatusOK)
	})
	stat, err := c.HeadObject(context.Background(), testProjABC, "assets", "a.png")
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if stat.Size != 42 || stat.ContentType != "image/png" || stat.ETag != "abc123" {
		t.Errorf("head: got %+v", stat)
	}
}

// The write time comes back too: it decides whether a confirmation is still
// in time, and whether the reaper may take the object.
func TestR2_HeadObject_ReturnsLastModified(t *testing.T) {
	written := time.Date(2026, 9, 20, 3, 0, 0, 0, time.UTC)
	c := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10")
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Last-Modified", written.Format(http.TimeFormat))
		w.WriteHeader(http.StatusOK)
	})
	stat, err := c.HeadObject(context.Background(), testProjABC, "assets", "a.txt")
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if !stat.LastModified.Equal(written) {
		t.Errorf("last modified: got %v, want %v", stat.LastModified, written)
	}
}

// A key that is not there is a sentinel the confirm path acts on, not an
// unexplained failure.
func TestR2_HeadObject_MissingKeyIsSentinel(t *testing.T) {
	c := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	_, err := c.HeadObject(context.Background(), testProjABC, "assets", "ghost.txt")
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("want ErrObjectNotFound, got %v", err)
	}
}
