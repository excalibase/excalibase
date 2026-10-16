package cfworkers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	testAccount   = "acct123"
	testNamespace = "sites-ns"
	testToken     = "cf-api-token-never-shown"
	testScript    = "site-abc"
)

// fakeCloudflare records what the client sent and answers like the Workers API.
type fakeCloudflare struct {
	mu       sync.Mutex
	requests []recorded
	handle   func(w http.ResponseWriter, r *http.Request, body []byte)
}

type recorded struct {
	method, path, query, auth, contentType string
	body                                   []byte
}

func newFake(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body []byte)) (*fakeCloudflare, *Client) {
	t.Helper()
	fake := &fakeCloudflare{handle: handle}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		fake.requests = append(fake.requests, recorded{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), body})
		fake.mu.Unlock()
		fake.handle(w, r, body)
	}))
	t.Cleanup(server.Close)
	client, err := New(Config{BaseURL: server.URL, AccountID: testAccount, APIToken: testToken, Namespace: testNamespace})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return fake, client
}

func writeEnvelope(w http.ResponseWriter, status int, result any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"success": status < 300, "errors": []any{}, "result": result})
}

func TestNewRefusesMissingSettings(t *testing.T) {
	for _, cfg := range []Config{
		{AccountID: "", APIToken: "t", Namespace: "n"},
		{AccountID: "a", APIToken: "", Namespace: "n"},
		{AccountID: "a", APIToken: "t", Namespace: ""},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("New(%+v) accepted a missing setting", cfg)
		}
	}
}

func TestStartUploadSessionSendsTheManifest(t *testing.T) {
	fake, client := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeEnvelope(w, http.StatusOK, map[string]any{"jwt": "upload-jwt", "buckets": [][]string{{"aaa"}, {"bbb"}}})
	})
	session, err := client.StartUploadSession(context.Background(), testScript, map[string]ManifestEntry{
		"/index.html": {Hash: "aaa", Size: 3},
	})
	if err != nil {
		t.Fatalf("StartUploadSession: %v", err)
	}
	if session.JWT != "upload-jwt" || len(session.Buckets) != 2 {
		t.Fatalf("session = %+v", session)
	}
	got := fake.requests[0]
	wantPath := "/client/v4/accounts/acct123/workers/dispatch/namespaces/sites-ns/scripts/site-abc/assets-upload-session"
	if got.method != http.MethodPost || got.path != wantPath {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
	if got.auth != "Bearer "+testToken {
		t.Errorf("auth = %q", got.auth)
	}
	var body struct {
		Manifest map[string]ManifestEntry `json:"manifest"`
	}
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body.Manifest["/index.html"].Hash != "aaa" || body.Manifest["/index.html"].Size != 3 {
		t.Errorf("manifest = %+v", body.Manifest)
	}
}

func TestUploadBucketSendsBase64PartsWithTheUploadToken(t *testing.T) {
	var parts = map[string]string{}
	var types = map[string]string{}
	fake, client := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("content type: %v", err)
		}
		reader := multipart.NewReader(strings.NewReader(string(body)), params["boundary"])
		for {
			part, err := reader.NextPart()
			if err != nil {
				break
			}
			content, _ := io.ReadAll(part)
			parts[part.FormName()] = string(content)
			types[part.FormName()] = part.Header.Get("Content-Type")
		}
		writeEnvelope(w, http.StatusCreated, map[string]any{"jwt": "completion-jwt"})
	})
	completion, err := client.UploadBucket(context.Background(), "upload-jwt", []Asset{
		{Hash: "aaa", ContentType: "text/html; charset=utf-8", Open: openString("<p>hi</p>")},
	})
	if err != nil {
		t.Fatalf("UploadBucket: %v", err)
	}
	if completion != "completion-jwt" {
		t.Errorf("completion = %q", completion)
	}
	got := fake.requests[0]
	if got.path != "/client/v4/accounts/acct123/workers/assets/upload" || got.query != "base64=true" {
		t.Errorf("request = %s?%s", got.path, got.query)
	}
	if got.auth != "Bearer upload-jwt" {
		t.Errorf("auth = %q, want the upload token, not the API token", got.auth)
	}
	if parts["aaa"] != base64.StdEncoding.EncodeToString([]byte("<p>hi</p>")) {
		t.Errorf("part = %q", parts["aaa"])
	}
	if types["aaa"] != "text/html; charset=utf-8" {
		t.Errorf("part type = %q", types["aaa"])
	}
}

func TestUploadBucketBeforeTheLastAnswersNoCompletionToken(t *testing.T) {
	_, client := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeEnvelope(w, http.StatusAccepted, map[string]any{"jwt": nil})
	})
	completion, err := client.UploadBucket(context.Background(), "upload-jwt", []Asset{{Hash: "a", ContentType: "text/plain", Open: openString("x")}})
	if err != nil || completion != "" {
		t.Fatalf("completion=%q err=%v, want none yet", completion, err)
	}
}

func TestUploadBucketReportsAFileThatCannotBeOpened(t *testing.T) {
	_, client := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeEnvelope(w, http.StatusCreated, map[string]any{"jwt": "c"})
	})
	failing := func() (io.ReadCloser, error) { return nil, errors.New("spool gone") }
	if _, err := client.UploadBucket(context.Background(), "upload-jwt", []Asset{{Hash: "a", ContentType: "text/plain", Open: failing}}); err == nil {
		t.Fatal("want the open failure reported")
	}
}

func TestPutScriptPublishesAnAssetsOnlyWorker(t *testing.T) {
	var metadata map[string]any
	var partNames []string
	fake, client := newFake(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		_, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		reader := multipart.NewReader(strings.NewReader(string(body)), params["boundary"])
		for {
			part, err := reader.NextPart()
			if err != nil {
				break
			}
			partNames = append(partNames, part.FormName())
			content, _ := io.ReadAll(part)
			if part.FormName() == "metadata" {
				_ = json.Unmarshal(content, &metadata)
			}
		}
		writeEnvelope(w, http.StatusOK, map[string]any{"id": testScript})
	})
	err := client.PutScript(context.Background(), testScript, "completion-jwt", AssetConfig{HTMLHandling: HTMLAutoTrailingSlash, NotFoundHandling: NotFoundSinglePage})
	if err != nil {
		t.Fatalf("PutScript: %v", err)
	}
	got := fake.requests[0]
	if got.method != http.MethodPut || got.path != "/client/v4/accounts/acct123/workers/dispatch/namespaces/sites-ns/scripts/site-abc" {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
	if got.auth != "Bearer "+testToken {
		t.Errorf("auth = %q", got.auth)
	}
	if strings.Join(partNames, ",") != "metadata" {
		t.Errorf("parts = %v, want only metadata", partNames)
	}
	if _, hasModule := metadata["main_module"]; hasModule || metadata["compatibility_date"] == "" {
		t.Errorf("metadata = %v", metadata)
	}
	assets, _ := metadata["assets"].(map[string]any)
	config, _ := assets["config"].(map[string]any)
	if assets["jwt"] != "completion-jwt" || config["not_found_handling"] != "single-page-application" || config["html_handling"] != "auto-trailing-slash" {
		t.Errorf("assets = %v", assets)
	}
}

func TestEveryCallCarriesAUserAgent(t *testing.T) {
	var agent string
	_, client := newFake(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent = r.Header.Get("User-Agent")
		writeEnvelope(w, http.StatusOK, nil)
	})
	if err := client.DeleteScript(context.Background(), testScript); err != nil {
		t.Fatalf("DeleteScript: %v", err)
	}
	if !strings.HasPrefix(agent, "excalibase-sites/") {
		t.Fatalf("user agent = %q", agent)
	}
}

func TestDeleteScriptTreatsAMissingScriptAsGone(t *testing.T) {
	fake, client := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10007,"message":"script not found"}]}`))
	})
	if err := client.DeleteScript(context.Background(), testScript); err != nil {
		t.Fatalf("DeleteScript: %v", err)
	}
	got := fake.requests[0]
	if got.method != http.MethodDelete || got.path != "/client/v4/accounts/acct123/workers/dispatch/namespaces/sites-ns/scripts/site-abc" {
		t.Fatalf("request = %s %s", got.method, got.path)
	}
}

func TestErrorsCarryCloudflareMessageButNeverTheToken(t *testing.T) {
	_, client := newFake(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":10000,"message":"Authentication error"}]}`))
	})
	_, err := client.StartUploadSession(context.Background(), testScript, map[string]ManifestEntry{"/index.html": {Hash: "a", Size: 1}})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want an APIError", err)
	}
	if apiErr.Status != http.StatusForbidden || !strings.Contains(err.Error(), "Authentication error") {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error leaks the API token: %v", err)
	}
}

func TestUnreachableCloudflareNeverEchoesTheToken(t *testing.T) {
	client, err := New(Config{BaseURL: "http://127.0.0.1:1", AccountID: testAccount, APIToken: testToken, Namespace: testNamespace})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = client.DeleteScript(context.Background(), testScript)
	if err == nil || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestScriptNameIsEscapedIntoThePath(t *testing.T) {
	_, client := newFake(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeEnvelope(w, http.StatusOK, nil)
	})
	if err := client.DeleteScript(context.Background(), "../../tokens"); err == nil {
		t.Fatal("a script name that is not a plain name must be refused")
	}
}

func openString(content string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader(content)), nil }
}
