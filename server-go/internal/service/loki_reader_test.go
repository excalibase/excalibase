package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/logdrain"
)

func TestLokiReader_ReadLinesForwardAndMerged(t *testing.T) {
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query_range" {
			t.Errorf("path %s", r.URL.Path)
		}
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":{"result":[
			{"stream":{"pod":"p-2"},"values":[["300","third"],["100","first"]]},
			{"stream":{"pod":"p-1"},"values":[["200","second"],["bad","skipped"]]}]}}`))
	}))
	defer server.Close()
	selector := postgresLogSelector("org-proj-a", "proj-a")
	lines, err := NewLokiReader(server.URL, server.Client()).ReadLines(context.Background(), selector, 50, 400, 3)
	if err != nil {
		t.Fatalf("ReadLines: %v", err)
	}
	if len(lines) != 3 || lines[0].Line != "first" || lines[1].Line != "second" || lines[2].TS != 300 {
		t.Errorf("lines %+v", lines)
	}
	if got.Get("query") != selector || got.Get("start") != "50" || got.Get("end") != "400" ||
		got.Get("limit") != "3" || got.Get("direction") != "forward" {
		t.Errorf("query %v", got)
	}
}

func TestLokiReader_CountLines(t *testing.T) {
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/loki/api/v1/query" {
			t.Errorf("path %s", r.URL.Path)
		}
		got = r.URL.Query()
		_, _ = w.Write([]byte(`{"data":{"result":[{"metric":{},"value":[1761386400,"4242"]}]}}`))
	}))
	defer server.Close()
	selector := postgresLogSelector("org-proj-a", "proj-a")
	count, err := NewLokiReader(server.URL, server.Client()).CountLines(context.Background(), selector, 1_000_000_000, 61_500_000_000)
	if err != nil || count != 4242 {
		t.Fatalf("CountLines = %d, %v", count, err)
	}
	if got.Get("query") != "sum(count_over_time("+selector+"[61s]))" || got.Get("time") != "61500000000" {
		t.Errorf("query %v", got)
	}
}

func TestLokiReader_ErrorsDoNotPanic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	reader := NewLokiReader(server.URL, server.Client())
	if _, err := reader.ReadLines(context.Background(), "{a=\"b\"}", 1, 2, 1); err == nil {
		t.Error("want an error")
	}
	if _, err := reader.CountLines(context.Background(), "{a=\"b\"}", 1, 2); err == nil {
		t.Error("want an error")
	}
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"result":[]}}`))
	}))
	defer empty.Close()
	if count, err := NewLokiReader(empty.URL, empty.Client()).CountLines(context.Background(), "{a=\"b\"}", 1, 2); err != nil || count != 0 {
		t.Errorf("empty count = %d, %v", count, err)
	}
}

// A line far over the cap is cut while it is decoded, and the batch keeps
// only what fits.
func TestLokiReader_CutsLongLines(t *testing.T) {
	long := strings.Repeat("<", 200_000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[{"stream":{"a":"b"},"values":[["1","` + long + `"],["2","short"]]}],"stats":{"x":1}}}`))
	}))
	defer server.Close()
	lines, err := NewLokiReader(server.URL, server.Client()).ReadLines(context.Background(), `{a="b"}`, 0, 10, 10)
	if err != nil || len(lines) != 2 {
		t.Fatalf("lines %d, %v", len(lines), err)
	}
	if len(lines[0].Line) != logdrain.MaxLineBytes || lines[1].Line != "short" {
		t.Errorf("line lengths %d %q", len(lines[0].Line), lines[1].Line)
	}
}

func TestLokiReader_MalformedAnswerIsAnError(t *testing.T) {
	for _, body := range []string{`[]`, `{"data":{"result":{}}}`, `{"data":`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		if _, err := NewLokiReader(server.URL, server.Client()).ReadLines(context.Background(), `{a="b"}`, 0, 10, 10); err == nil {
			t.Errorf("%s: no error", body)
		}
		server.Close()
	}
}
