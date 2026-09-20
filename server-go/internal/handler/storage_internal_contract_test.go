package handler

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The internal storage routes are a contract with the Deno edge runtime,
// which lives in this repo but is not compiled with it: nothing here fails
// when a field is renamed, added or dropped on either side. These tests are
// that signal.
//
// The runtime's half of the contract is in deno-server/server.ts
// (dispatchStorage) and deno-server/runtime/storage.ts; its own tests pin the
// same field names from the other direction.
const updateRuntimeToo = "the Deno runtime speaks this shape — update " +
	"deno-server/server.ts (dispatchStorage), deno-server/runtime/storage.ts, " +
	"the vendored lib in deno-server/lib/excalibase-server, and their tests"

// jsonFieldNames lists the wire names a struct serialises to, so a rename or
// a dropped field is visible rather than silently changing the protocol.
func jsonFieldNames(t *testing.T, v any) []string {
	t.Helper()
	typ := reflect.TypeOf(v)
	names := []string{}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = typ.Field(i).Name
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func assertFields(t *testing.T, what string, v any, want []string) {
	t.Helper()
	got := jsonFieldNames(t, v)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s wire shape changed: got %v, want %v\n%s", what, got, want, updateRuntimeToo)
	}
}

func TestInternalStorageContract_UploadURLShapes(t *testing.T) {
	assertFields(t, "POST /internal/storage/{p}/upload-url request",
		internalUploadURLRequest{}, []string{"contentType", "size"})
	assertFields(t, "POST /internal/storage/{p}/upload-url response",
		internalUploadURLResponse{}, []string{"storageId", "uploadId", "url", "method", "headers"})
}

func TestInternalStorageContract_ConfirmUploadShape(t *testing.T) {
	// Size and contentType are still accepted on the wire for callers that
	// send them, but they decide nothing: the object store is read back. The
	// upload id is what names the bytes being accepted.
	assertFields(t, "POST /internal/storage/{p}/confirm-upload request",
		internalConfirmUploadRequest{}, []string{"storageId", "uploadId", "contentType", "size", "sha256", "etag"})
}

func TestInternalStorageContract_DownloadAndMetadataShapes(t *testing.T) {
	assertFields(t, "POST /internal/storage/{p}/download-url request",
		internalDownloadURLRequest{}, []string{"storageId"})
	assertFields(t, "POST /internal/storage/{p}/download-url response",
		internalDownloadURLResponse{}, []string{"url"})
	assertFields(t, "GET /internal/storage/{p}/metadata/{id} response",
		internalMetadataResponse{}, []string{"storageId", "sha256", "size", "contentType"})
}

// A confirmation that omits the upload id is refused, which is what makes a
// caller that was not updated fail loudly instead of silently recording
// nothing.
func TestInternalStorageContract_ConfirmWithoutAnUploadIDIsRefused(t *testing.T) {
	r, _, _ := newStorageInternalRouterWithBackend(t, "the-secret")
	req := httptest.NewRequest("POST", "/internal/storage/"+testStorageProjectID+"/confirm-upload",
		strings.NewReader(`{"storageId":"kg2_a","contentType":"text/plain","size":4}`))
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a confirmation with no upload id: want 400, got %d (body=%s)\n%s",
			w.Code, w.Body.String(), updateRuntimeToo)
	}
}

// The mint route refuses a body that declares neither size nor type, which is
// the shape a caller on the old contract would send.
func TestInternalStorageContract_UploadURLWithoutMetadataIsRefused(t *testing.T) {
	r, _, _ := newStorageInternalRouterWithBackend(t, "the-secret")
	req := httptest.NewRequest("POST", "/internal/storage/"+testStorageProjectID+"/upload-url",
		strings.NewReader(`{}`))
	req.Header.Set(runtimeTokenHeader, testStorageRuntimeToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a mint with no declared size or type: want 400, got %d (body=%s)\n%s",
			w.Code, w.Body.String(), updateRuntimeToo)
	}
}
