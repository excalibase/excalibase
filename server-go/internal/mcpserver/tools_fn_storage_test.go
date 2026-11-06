package mcpserver

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/features"
	custommw "github.com/excalibase/provisioning-poc/internal/middleware"
	"github.com/excalibase/provisioning-poc/internal/storagesvc"
)

var storageNow = time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC)

func photosBucket() storagesvc.Bucket {
	return storagesvc.Bucket{
		ID: "bkt_1", ProjectID: testProjectA, Name: "photos", Status: "active", FileSize: 1048576, AllowedTypes: []string{"image/png"},
		Access:    storagesvc.BucketAccess{"user": {Read: storagesvc.ScopeOwn, Write: storagesvc.ScopeOwn}},
		CreatedAt: storageNow, UpdatedAt: storageNow,
	}
}

func fnStorageRouteCases() []routeCase {
	appStorage := []features.Feature{features.AppStorage}
	return []routeCase{
		{
			name: "invoke_function", tool: "invoke_function", args: map[string]any{"project_id": testProjectA, "function_id": "hello", "body": map[string]any{"args": map[string]any{"name": "Ada"}}},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPost, projectsA+"/functions/hello/invoke", 200, `{"data":"hi Ada"}`)
			},
			expect: []string{"POST " + projectsA + "/functions/hello/invoke"},
			check: func(t *testing.T, calls []recordedCall, out map[string]any) {
				if calls[0].Body != `{"args":{"name":"Ada"}}` {
					t.Errorf("body = %s", calls[0].Body)
				}
				if out["status"] != float64(200) || out["body"] != `{"data":"hi Ada"}` {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "list_function_secrets", tool: "list_function_secrets", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/functions/secrets", 200, `[{"key":"STRIPE_KEY"},{"key":"RESEND_KEY"}]`)
			},
			expect: []string{"GET " + projectsA + "/functions/secrets"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				names, _ := out["names"].([]any)
				if len(names) != 2 || names[0] != "STRIPE_KEY" {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "delete_function_secret", tool: "delete_function_secret", args: map[string]any{"project_id": testProjectA, "key": "STRIPE_KEY"},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodDelete, projectsA+"/functions/secrets/STRIPE_KEY", 200, `{"status":"deleted","key":"STRIPE_KEY"}`)
			},
			expect: []string{"DELETE " + projectsA + "/functions/secrets/STRIPE_KEY"},
		},
		{
			name: "list_storage_buckets", tool: "list_storage_buckets", args: map[string]any{"project_id": testProjectA},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/storage/buckets", 200, "["+realJSON(photosBucket(), plantedFields)+"]")
			},
			expect: []string{"GET " + projectsA + "/storage/buckets"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				buckets, _ := out["buckets"].([]any)
				if len(buckets) != 1 {
					t.Fatalf("out = %v", out)
				}
				bucket, _ := buckets[0].(map[string]any)
				if bucket["name"] != "photos" || bucket["public"] != false || bucket["fileSizeLimit"] != float64(1048576) {
					t.Errorf("bucket = %v", bucket)
				}
				for _, hidden := range []string{"plantedSecret", "nested", "id", "projectId"} {
					if _, ok := bucket[hidden]; ok {
						t.Errorf("bucket carries %s", hidden)
					}
				}
			},
		},
		{
			name: "list_storage_objects", tool: "list_storage_objects", args: map[string]any{"project_id": testProjectA, "bucket": "photos", "prefix": "avatars/", "limit": 20},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodGet, projectsA+"/storage/buckets/photos/objects", 200, realJSON(storagesvc.ListObjectsResponse{
					Objects:    []storagesvc.Object{{ID: "o1", BucketID: "bkt_1", Key: "avatars/a.png", Size: 12, MimeType: "image/png", ETag: "e", OwnerID: "user-9", CreatedAt: storageNow, UpdatedAt: storageNow}},
					NextCursor: "c2",
				}, plantedFields))
			},
			expect: []string{"GET " + projectsA + "/storage/buckets/photos/objects?limit=20&prefix=avatars%2F"},
			check: func(t *testing.T, _ []recordedCall, out map[string]any) {
				objects, _ := out["objects"].([]any)
				if len(objects) != 1 || out["nextCursor"] != "c2" {
					t.Fatalf("out = %v", out)
				}
				object, _ := objects[0].(map[string]any)
				if object["key"] != "avatars/a.png" || object["size"] != float64(12) {
					t.Errorf("object = %v", object)
				}
				for _, hidden := range []string{"plantedSecret", "nested", "ownerId", "id", "bucketId", "etag"} {
					if _, ok := object[hidden]; ok {
						t.Errorf("object carries %s", hidden)
					}
				}
				if _, ok := out["plantedSecret"]; ok {
					t.Error("the page carries a field no view names")
				}
			},
		},
		{
			name: "create_storage_bucket", tool: "create_storage_bucket",
			args: map[string]any{"project_id": testProjectA, "name": "photos", "file_size_limit": 1048576, "allowed_mime_types": []any{"image/png"}},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPost, projectsA+"/storage/buckets", 201, realJSON(photosBucket(), plantedFields))
			},
			expect: []string{"POST " + projectsA + "/storage/buckets"},
			check: func(t *testing.T, calls []recordedCall, out map[string]any) {
				if calls[0].Body != `{"allowedMimeTypes":["image/png"],"fileSizeLimit":1048576,"name":"photos"}` {
					t.Errorf("body = %s", calls[0].Body)
				}
				bucket, _ := out["bucket"].(map[string]any)
				if bucket["name"] != "photos" || strings.Contains(realJSON(out, nil), "plantedSecret") {
					t.Errorf("out = %v", out)
				}
			},
		},
		{
			name: "set_bucket_access", tool: "set_bucket_access", features: appStorage,
			args: map[string]any{"project_id": testProjectA, "bucket": "photos", "access": map[string]any{"user": map[string]any{"read": "own", "write": "own"}}},
			setup: func(f *fakeRoutes) {
				f.on(http.MethodPut, projectsA+"/storage/buckets/photos/access", 200, realJSON(photosBucket(), plantedFields))
			},
			expect: []string{"PUT " + projectsA + "/storage/buckets/photos/access"},
			check: func(t *testing.T, calls []recordedCall, out map[string]any) {
				if calls[0].Body != `{"access":{"user":{"read":"own","write":"own"}}}` {
					t.Errorf("body = %s", calls[0].Body)
				}
				bucket, _ := out["bucket"].(map[string]any)
				if access, _ := bucket["access"].(map[string]any); access["user"] == nil || strings.Contains(realJSON(out, nil), "plantedSecret") {
					t.Errorf("out = %v", out)
				}
			},
		},
	}
}

// The function's own answer is shown whatever its status, and told apart from a refusal by the platform.
func TestInvokeFunctionShowsTheFunctionsOwnFailure(t *testing.T) {
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(custommw.FunctionResponseHeader, "1")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom: db unreachable"))
	})
	cs := session(t, router, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "invoke_function", map[string]any{"project_id": testProjectA, "function_id": "hello"}))
	if out["status"] != float64(500) || out["body"] != "boom: db unreachable" || out["contentType"] != "text/plain" {
		t.Fatalf("out = %v", out)
	}
}

func TestInvokeFunctionExplainsAPlatformRefusal(t *testing.T) {
	f := newFakeRoutes()
	f.on(http.MethodPost, projectsA+"/functions/hello/invoke", 404, `{"error":"function not found"}`)
	res := callTool(t, session(t, f, &recordingAudit{}, writeCaller()), "invoke_function", map[string]any{"project_id": testProjectA, "function_id": "hello"})
	if !res.IsError || !strings.Contains(resultText(res), "function not found") {
		t.Fatalf("result = %s", resultText(res))
	}
}

func TestInvokeFunctionBoundsWhatItReturns(t *testing.T) {
	router := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(custommw.FunctionResponseHeader, "1")
		_, _ = w.Write([]byte(strings.Repeat("a", maxInvokeBodyShown+500)))
	})
	cs := session(t, router, &recordingAudit{}, writeCaller())
	out := structured(t, callTool(t, cs, "invoke_function", map[string]any{"project_id": testProjectA, "function_id": "hello"}))
	body, _ := out["body"].(string)
	if len(body) != maxInvokeBodyShown || out["truncated"] != true {
		t.Fatalf("body %d bytes, truncated %v", len(body), out["truncated"])
	}
}

func TestStorageToolsRefuseNamesThatClimb(t *testing.T) {
	f := newFakeRoutes()
	cs := session(t, f, &recordingAudit{}, writeCaller())
	for _, tool := range []string{"list_storage_objects"} {
		for _, name := range []string{"", ".."} {
			if res := callTool(t, cs, tool, map[string]any{"project_id": testProjectA, "bucket": name}); !res.IsError {
				t.Errorf("%s accepted bucket %q", tool, name)
			}
		}
	}
	if res := callTool(t, cs, "create_storage_bucket", map[string]any{"project_id": testProjectA, "name": ""}); !res.IsError {
		t.Error("create_storage_bucket accepted an empty name")
	}
	if len(f.calls) != 0 {
		t.Errorf("calls = %v", f.calls)
	}
}
