package logdelete

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const selA = `{namespace="org1-proj-a"}`

func TestLokiClient_RequestSendsTheSelectorAndWindow(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	client := NewLokiClient(srv.URL+"/", srv.Client())
	start := time.Unix(1700000000, 0)
	end := time.Unix(1700086400, 0)
	if err := client.Request(context.Background(), selA, start, end); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/loki/api/v1/delete" {
		t.Fatalf("request %s %s", got.Method, got.URL.Path)
	}
	query := got.URL.Query()
	if query.Get("query") != selA || query.Get("start") != "1700000000" || query.Get("end") != "1700086400" {
		t.Errorf("params = %v", query)
	}
}

// Nothing reaches Loki unless the selector is one exact project label.
func TestLokiClient_RefusesAnUnsafeSelectorWithoutCallingLoki(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	client := NewLokiClient(srv.URL, srv.Client())
	for _, bad := range []string{``, `{}`, `{namespace=~".+"}`} {
		if err := client.Request(context.Background(), bad, time.Unix(1700000000, 0), time.Now()); !errors.Is(err, ErrUnsafeSelector) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if called {
		t.Error("Loki was called with an unsafe selector")
	}
}

// A refusal is an error naming the status only: Loki's answer is not repeated.
func TestLokiClient_RequestRefusedIsAnErrorWithoutTheBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "secret internal detail", http.StatusBadRequest)
	}))
	defer srv.Close()
	err := NewLokiClient(srv.URL, srv.Client()).Request(context.Background(), selA, time.Unix(1700000000, 0), time.Now())
	if err == nil || !strings.Contains(err.Error(), "400") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err = %v", err)
	}
}

func TestLokiClient_RequestUnreachableIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	if err := NewLokiClient(srv.URL, http.DefaultClient).Request(context.Background(), selA, time.Unix(1700000000, 0), time.Now()); err == nil {
		t.Fatal("a closed Loki accepted the request")
	}
}

func statusServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/loki/api/v1/delete" {
			t.Errorf("status read %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLokiClient_StatusMatchesTheQueryAndStart(t *testing.T) {
	start := time.Unix(1704067200, 0)
	cases := []struct {
		name string
		body string
		want Status
	}{
		{"none", `[]`, StatusMissing},
		{"another project only", `[{"query":"{namespace=\"org1-proj-b\"}","start_time":1704067200,"status":"processed"}]`, StatusMissing},
		{"an older request for the same id", `[{"query":"{namespace=\"org1-proj-a\"}","start_time":1600000000,"status":"processed"}]`, StatusMissing},
		{"received", `[{"query":"{namespace=\"org1-proj-a\"}","start_time":1704067200,"end_time":1793322969.007,"status":"received"}]`, StatusReceived},
		{"processed", `[{"query":"{namespace=\"org1-proj-a\"}","start_time":1704067200.4,"status":"processed"}]`, StatusProcessed},
		{"one shard still open", `[{"query":"{namespace=\"org1-proj-a\"}","start_time":1704067200,"status":"processed"},{"query":"{namespace=\"org1-proj-a\"}","start_time":1704067200,"status":"received"}]`, StatusReceived},
		{"open shard listed first", `[{"query":"{namespace=\"org1-proj-a\"}","start_time":1704067200,"status":"received"},{"query":"{namespace=\"org1-proj-a\"}","start_time":1704067200,"status":"processed"}]`, StatusReceived},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := statusServer(t, http.StatusOK, tc.body)
			statuses, err := NewLokiClient(srv.URL, srv.Client()).Statuses(context.Background())
			if got := statuses.Of(selA, start); err != nil || got != tc.want {
				t.Errorf("status = %v %v, want %v", got, err, tc.want)
			}
		})
	}
}

func TestLokiClient_StatusErrors(t *testing.T) {
	for name, srv := range map[string]*httptest.Server{
		"5xx":       statusServer(t, http.StatusInternalServerError, "boom"),
		"not json":  statusServer(t, http.StatusOK, "<html>"),
		"not array": statusServer(t, http.StatusOK, `{"a":1}`),
	} {
		if _, err := NewLokiClient(srv.URL, srv.Client()).Statuses(context.Background()); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
