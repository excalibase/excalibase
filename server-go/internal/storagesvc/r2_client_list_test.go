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

// An upload becomes an object by being copied onto its key, server-side.
func TestR2_CopyObject_CopiesWithinTheBucket(t *testing.T) {
	var gotSource, gotPath string
	c := newStubbedR2(t, func(w http.ResponseWriter, r *http.Request) {
		gotSource = r.Header.Get("X-Amz-Copy-Source")
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`<CopyObjectResult><ETag>"e"</ETag></CopyObjectResult>`))
	})
	if err := c.CopyObject(context.Background(), testProjABC, "bkt_1", ".staging/upl_1", "a.txt"); err != nil {
		t.Fatalf("CopyObject: %v", err)
	}
	if !strings.Contains(gotSource, "upl_1") {
		t.Errorf("copy source: %q", gotSource)
	}
	if !strings.HasSuffix(gotPath, "/buckets/bkt_1/a.txt") {
		t.Errorf("copy destination: %q", gotPath)
	}
}

func TestR2_CopyObject_RejectsBadKeys(t *testing.T) {
	c := newR2(t)
	if err := c.CopyObject(context.Background(), testProjABC, "bkt_1", "..", "a.txt"); err == nil {
		t.Error("a traversal source must be refused")
	}
	if err := c.CopyObject(context.Background(), testProjABC, "bkt_1", "a.txt", ".."); err == nil {
		t.Error("a traversal destination must be refused")
	}
}

// A source that is not there is the sentinel the confirm path acts on.
func TestR2_CopyObject_MissingSourceIsSentinel(t *testing.T) {
	c := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code></Error>`))
	})
	err := c.CopyObject(context.Background(), testProjABC, "bkt_1", ".staging/upl_1", "a.txt")
	if !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("want ErrObjectNotFound, got %v", err)
	}
}

func TestR2_CopyObject_ReportsBackendError(t *testing.T) {
	c := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
	})
	err := c.CopyObject(context.Background(), testProjABC, "bkt_1", ".staging/upl_1", "a.txt")
	if err == nil || !strings.Contains(err.Error(), "copy object") {
		t.Fatalf("a refused copy must surface, got %v", err)
	}
}

// The reaper's listing sees the staging namespace and nothing else, and the
// ids it returns are bare — a live key could not be expressed as one.
func TestR2_ListStagedUploads_ListsOnlyTheStagingNamespace(t *testing.T) {
	var gotPrefix string
	c := newStubbedR2(t, func(w http.ResponseWriter, r *http.Request) {
		gotPrefix = r.URL.Query().Get("prefix")
		_, _ = w.Write([]byte(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Contents><Key>projects/proj-abc/buckets/bkt_1/.staging/upl_1</Key><LastModified>2026-09-20T03:00:00Z</LastModified></Contents>
</ListBucketResult>`))
	})
	staged, err := c.ListStagedUploads(context.Background(), testProjABC, "bkt_1", 0)
	if err != nil {
		t.Fatalf("ListStagedUploads: %v", err)
	}
	if want := "projects/proj-abc/buckets/bkt_1/.staging/"; gotPrefix != want {
		t.Errorf("listing prefix: got %q, want %q", gotPrefix, want)
	}
	if len(staged) != 1 || staged[0].UploadID != "upl_1" {
		t.Fatalf("unexpected staged uploads: %+v", staged)
	}
	if staged[0].LastModified.IsZero() {
		t.Error("the write time decides whether an upload is abandoned")
	}
}

func TestR2_ListStagedUploads_RejectsMissingBucket(t *testing.T) {
	c := newR2(t)
	if _, err := c.ListStagedUploads(context.Background(), testProjABC, "", 10); err == nil {
		t.Error("listing without a bucket must fail before any network call")
	}
}

func TestR2_ListStagedUploads_ReportsBackendError(t *testing.T) {
	c := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
	})
	if _, err := c.ListStagedUploads(context.Background(), testProjABC, "bkt_1", 5); err == nil {
		t.Error("a refused listing must not read as an empty staging area")
	}
}

// The key is built from the id, so an id that could carry a path is refused.
func TestR2_DeleteStagingObject_RefusesAnIDThatCouldCarryAPath(t *testing.T) {
	c := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a suspect upload id must never reach the store")
		w.WriteHeader(http.StatusNoContent)
	})
	for _, id := range []string{"", "../a", "a/b", `a\b`} {
		if err := c.DeleteStagingObject(context.Background(), testProjABC, "bkt_1", id); err == nil {
			t.Errorf("upload id %q must be refused", id)
		}
	}
}

func TestR2_DeleteStagingObject_DeletesTheStagedKey(t *testing.T) {
	var gotPath string
	c := newStubbedR2(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.DeleteStagingObject(context.Background(), testProjABC, "bkt_1", "upl_1"); err != nil {
		t.Fatalf("DeleteStagingObject: %v", err)
	}
	if !strings.HasSuffix(gotPath, "/buckets/bkt_1/.staging/upl_1") {
		t.Errorf("delete hit %q", gotPath)
	}
}

// A project teardown works from the prefix alone: by then the catalogue rows
// are gone with the project's record.
func TestR2_ListKeysWithPrefix_ReturnsWholeStoreKeys(t *testing.T) {
	var gotPrefix string
	c := newStubbedR2(t, func(w http.ResponseWriter, r *http.Request) {
		gotPrefix = r.URL.Query().Get("prefix")
		_, _ = w.Write([]byte(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">
  <Contents><Key>projects/proj-abc/buckets/bkt_1/a.txt</Key></Contents>
  <Contents><Key>projects/proj-abc/buckets/bkt_2/b.txt</Key></Contents>
</ListBucketResult>`))
	})

	keys, err := c.ListKeysWithPrefix(context.Background(), "projects/proj-abc/", 10)
	if err != nil {
		t.Fatalf("ListKeysWithPrefix: %v", err)
	}
	if len(keys) != 2 || keys[0] != "projects/proj-abc/buckets/bkt_1/a.txt" {
		t.Errorf("unexpected keys: %v", keys)
	}
	if gotPrefix != "projects/proj-abc/" {
		t.Errorf("listing prefix: got %q", gotPrefix)
	}
}

func TestR2_ListKeysWithPrefix_RequiresAPrefix(t *testing.T) {
	c := newR2(t)
	if _, err := c.ListKeysWithPrefix(context.Background(), "", 10); err == nil {
		t.Error("an empty prefix would name the whole bucket and must be refused")
	}
}

func TestR2_ListKeysWithPrefix_DefaultsLimit(t *testing.T) {
	var gotMaxKeys string
	c := newStubbedR2(t, func(w http.ResponseWriter, r *http.Request) {
		gotMaxKeys = r.URL.Query().Get("max-keys")
		_, _ = w.Write([]byte(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"></ListBucketResult>`))
	})
	if _, err := c.ListKeysWithPrefix(context.Background(), "projects/proj-abc/", 0); err != nil {
		t.Fatalf("ListKeysWithPrefix: %v", err)
	}
	if gotMaxKeys == "" || gotMaxKeys == "0" {
		t.Errorf("max-keys should default to a positive value, got %q", gotMaxKeys)
	}
}

func TestR2_ListKeysWithPrefix_ReportsBackendError(t *testing.T) {
	c := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
	})
	if _, err := c.ListKeysWithPrefix(context.Background(), "projects/proj-abc/", 5); err == nil {
		t.Error("a refused listing must not read as an empty prefix")
	}
}

// A delete works from what a listing returned, so a key outside the namespace
// the caller asked about is refused before it reaches the store.
func TestR2_DeleteKey_RefusesAKeyOutsideThePrefix(t *testing.T) {
	c := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("a key outside the prefix must never reach the store")
		w.WriteHeader(http.StatusNoContent)
	})
	err := c.DeleteKey(context.Background(), "projects/proj-abc/", "projects/other/buckets/b/x.txt")
	if err == nil {
		t.Fatal("a key outside the prefix must be refused")
	}
	if err := c.DeleteKey(context.Background(), "", "anything"); err == nil {
		t.Error("an empty prefix must be refused")
	}
}

func TestR2_DeleteKey_DeletesAndToleratesAMissingKey(t *testing.T) {
	var gotPath string
	c := newStubbedR2(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	key := "projects/proj-abc/buckets/bkt_1/a.txt"
	if err := c.DeleteKey(context.Background(), "projects/proj-abc/", key); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if !strings.HasSuffix(gotPath, key) {
		t.Errorf("delete hit %q, want the listed key", gotPath)
	}

	missing := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code></Error>`))
	})
	if err := missing.DeleteKey(context.Background(), "projects/proj-abc/", key); err != nil {
		t.Errorf("an already-deleted key should count as deleted: %v", err)
	}
}

func TestR2_DeleteKey_ReportsBackendError(t *testing.T) {
	c := newStubbedR2(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code></Error>`))
	})
	err := c.DeleteKey(context.Background(), "projects/proj-abc/", "projects/proj-abc/buckets/b/x.txt")
	if err == nil || !strings.Contains(err.Error(), "delete object") {
		t.Fatalf("a refused delete must surface, got %v", err)
	}
}
