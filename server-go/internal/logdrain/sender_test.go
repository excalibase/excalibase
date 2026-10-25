package logdrain

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/publicaddr"
	"github.com/excalibase/provisioning-poc/internal/testutil"
)

var testSecret = testutil.FixtureSecret("dd-key")

var sampleLines = []Line{
	{TS: time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC).UnixNano(), Line: "LOG:  checkpoint starting"},
	{TS: time.Date(2026, 10, 25, 10, 0, 1, 500, time.UTC).UnixNano(), Line: `ERROR:  relation "x" does not exist`},
}

// testSender talks to a local HTTPS test server: its client trusts the test
// certificate and the port check names the server's port.
func testSender(server *httptest.Server) *Sender {
	sender := NewSender(server.Client())
	sender.targetCheck = httpsOnly
	sender.datadogTarget = func(string) bool { return true }
	return sender
}

// httpsOnly stands in for ParseEndpoint where the destination is a loopback test server.
func httpsOnly(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" {
		return ErrInvalidDrain
	}
	return nil
}

func TestSender_DatadogBatch(t *testing.T) {
	var got *http.Request
	var body []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	err := testSender(server).Send(context.Background(), Batch{
		DrainID: "ld_1", ProjectID: "proj-a", Kind: KindDatadog, Target: server.URL + "/api/v2/logs",
		Secret: testSecret, Lines: sampleLines,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/api/v2/logs" {
		t.Errorf("request %s %s", got.Method, got.URL.Path)
	}
	if got.Header.Get("DD-API-KEY") != testSecret || got.Header.Get("Content-Type") != "application/json" {
		t.Errorf("headers %v", got.Header)
	}
	var entries []map[string]any
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("body is not a JSON array: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d", len(entries))
	}
	first := entries[0]
	if first["message"] != sampleLines[0].Line || first["ddsource"] != "excalibase" || first["service"] != "proj-a" ||
		first["ddtags"] != "project:proj-a" || first["timestamp"] != float64(sampleLines[0].TS/int64(time.Millisecond)) {
		t.Errorf("entry %v", first)
	}
}

func TestSender_HTTPSBatchWithCustomHeader(t *testing.T) {
	var got *http.Request
	var body []byte
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
	}))
	defer server.Close()

	err := testSender(server).Send(context.Background(), Batch{
		DrainID: "ld_2", ProjectID: "proj-a", Kind: KindHTTPS, Target: server.URL + "/ingest?src=excalibase",
		HeaderName: "Authorization", Secret: "Bearer " + testSecret, Lines: sampleLines,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Header.Get("Authorization") != "Bearer "+testSecret || got.URL.RawQuery != "src=excalibase" {
		t.Errorf("request %v %v", got.Header, got.URL)
	}
	var entries []map[string]any
	if err := json.Unmarshal(body, &entries); err != nil || len(entries) != 2 {
		t.Fatalf("body %s: %v", body, err)
	}
	if entries[1]["message"] != sampleLines[1].Line || entries[1]["project"] != "proj-a" || entries[1]["source"] != "database" ||
		entries[1]["timestamp"] != "2026-10-25T10:00:01.0000005Z" {
		t.Errorf("entry %v", entries[1])
	}
}

func TestSender_HTTPSWithoutHeaderSendsNoCredential(t *testing.T) {
	var got http.Header
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { got = r.Header }))
	defer server.Close()
	if err := testSender(server).Send(context.Background(), Batch{Kind: KindHTTPS, Target: server.URL, Lines: sampleLines}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got.Get("Authorization") != "" || got.Get("DD-API-KEY") != "" {
		t.Errorf("headers %v", got)
	}
}

// A redirect is a failure: the batch and its key never follow it anywhere.
func TestSender_DoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Int32
	elsewhere := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	defer elsewhere.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	err := testSender(server).Send(context.Background(), Batch{Kind: KindDatadog, Target: server.URL, Secret: testSecret, Lines: sampleLines})
	var sendErr *SendError
	if !errors.As(err, &sendErr) || !strings.Contains(sendErr.Error(), "redirect") {
		t.Fatalf("err = %v, want a redirect refusal", err)
	}
	if followed.Load() != 0 {
		t.Error("the redirect was followed")
	}
}

// Errors name the cause, never the key, the URL's path and query or the
// destination's answer body.
func TestSender_ErrorsNeverCarrySecretsOrURL(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("invalid key " + testSecret))
	}))
	defer server.Close()
	target := server.URL + "/ingest?token=urltoken123"
	err := testSender(server).Send(context.Background(), Batch{Kind: KindHTTPS, Target: target, HeaderName: "X-Key", Secret: testSecret, Lines: sampleLines})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want the status", err)
	}
	assertClean(t, err.Error())

	server.Close()
	err = testSender(server).Send(context.Background(), Batch{Kind: KindHTTPS, Target: target, HeaderName: "X-Key", Secret: testSecret, Lines: sampleLines})
	if err == nil {
		t.Fatal("want a connect failure")
	}
	assertClean(t, err.Error())
}

func assertClean(t *testing.T, msg string) {
	t.Helper()
	for _, leaked := range []string{testSecret, "urltoken123", "/ingest", "127.0.0.1"} {
		if strings.Contains(msg, leaked) {
			t.Errorf("error %q leaks %q", msg, leaked)
		}
	}
}

func TestSender_ServerErrorIsRetryableFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	err := testSender(server).Send(context.Background(), Batch{Kind: KindHTTPS, Target: server.URL, Lines: sampleLines})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v", err)
	}
}

// The target is checked again before sending, whatever the control plane said.
func TestSender_RefusesNonHTTPSOrOtherPortTargets(t *testing.T) {
	sender := NewSender(http.DefaultClient)
	for _, target := range []string{"http://logs.example.com/", "https://logs.example.com:8443/", "https://u:p@logs.example.com/", "https://10.0.0.1/"} {
		if err := sender.Send(context.Background(), Batch{Kind: KindHTTPS, Target: target, Lines: sampleLines}); err == nil {
			t.Errorf("%s: sent", target)
		}
	}
	if err := sender.Send(context.Background(), Batch{Kind: "smtp", Target: "https://logs.example.com/", Lines: sampleLines}); err == nil {
		t.Error("unknown kind sent")
	}
}

// The production client never reaches a loopback destination, even one that
// otherwise answers perfectly.
func TestNewHTTPClient_RefusesLoopbackDestination(t *testing.T) {
	var reached atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	defer server.Close()
	sender := NewSender(NewHTTPClient(publicaddr.NewDialer(nil, nil)))
	sender.targetCheck = httpsOnly
	err := sender.Send(context.Background(), Batch{Kind: KindHTTPS, Target: server.URL, Lines: sampleLines})
	if err == nil || reached.Load() != 0 {
		t.Fatalf("err = %v, reached = %d", err, reached.Load())
	}
	if !strings.Contains(err.Error(), "not a public address") {
		t.Errorf("err = %v", err)
	}
}

func TestEncode_BoundsLineSize(t *testing.T) {
	huge := strings.Repeat("x", MaxLineBytes+100)
	body, err := encode(Batch{Kind: KindHTTPS, ProjectID: "p", Lines: []Line{{TS: 1, Line: huge}}})
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatal(err)
	}
	if msg := entries[0]["message"].(string); len(msg) > MaxLineBytes {
		t.Errorf("line not truncated: %d", len(msg))
	}
}

// A Datadog key only ever goes to an official intake host, whatever target arrives.
func TestSender_DatadogKeyOnlyToIntakeHosts(t *testing.T) {
	var reached atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Add(1) }))
	defer server.Close()
	sender := NewSender(server.Client())
	sender.targetCheck = httpsOnly
	err := sender.Send(context.Background(), Batch{Kind: KindDatadog, Target: server.URL + "/api/v2/logs", Secret: testSecret, Lines: sampleLines})
	if err == nil || reached.Load() != 0 {
		t.Fatalf("err = %v, reached %d", err, reached.Load())
	}
}

func TestEncode_DoesNotEscapeHTML(t *testing.T) {
	body, err := encode(Batch{Kind: KindHTTPS, ProjectID: "p", Lines: []Line{{TS: 1, Line: "<<<>>>&&&"}}})
	if err != nil || !strings.Contains(string(body), "<<<>>>&&&") {
		t.Errorf("body %s, %v", body, err)
	}
}

// An answer whose body never ends is a failure, so a slow destination cannot
// hold a worker and still count as delivered.
func TestSender_UnfinishedAnswerIsAFailure(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := testSender(server).Send(ctx, Batch{Kind: KindHTTPS, Target: server.URL, Lines: sampleLines})
	if err == nil {
		t.Fatal("want a failure")
	}
	var sendErr *SendError
	if !errors.As(err, &sendErr) {
		t.Errorf("err %T %v", err, err)
	}
}
