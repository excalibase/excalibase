package hostlogs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLokiPushSendsLabelsAndLinesAsLokiTakesThem(t *testing.T) {
	var got struct {
		Streams []struct {
			Stream map[string]string `json:"stream"`
			Values [][]string        `json:"values"`
		} `json:"streams"`
	}
	var contentType, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, contentType = r.URL.Path, r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("body %s: %v", body, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	pusher := NewLokiPusher(server.URL+"/", server.Client())
	err := pusher.Push(context.Background(), []Stream{{
		Labels:  Labels{ProjectID: "proj-a", Component: "database", Container: "excalibase-proj-a-postgres", Tier: "free"},
		Entries: []Entry{{At: 1761800000000000001, Line: `{"x":"} {project_id=\"proj-b\"}"`}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if path != "/loki/api/v1/push" || contentType != "application/json" {
		t.Errorf("path %s type %s", path, contentType)
	}
	if len(got.Streams) != 1 || len(got.Streams[0].Stream) != 4 || got.Streams[0].Stream["project_id"] != "proj-a" || got.Streams[0].Stream["tier"] != "free" {
		t.Fatalf("streams = %+v", got.Streams)
	}
	if v := got.Streams[0].Values[0]; v[0] != "1761800000000000001" || v[1] != `{"x":"} {project_id=\"proj-b\"}"` {
		t.Errorf("value = %v", v)
	}
}

func TestLokiPushWithoutATierSendsNoTierLabel(t *testing.T) {
	var stream map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Streams []struct {
				Stream map[string]string `json:"stream"`
			} `json:"streams"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		stream = body.Streams[0].Stream
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	_ = NewLokiPusher(server.URL, server.Client()).Push(context.Background(), []Stream{{
		Labels: Labels{ProjectID: "proj-a", Component: "app", Container: "c"}, Entries: []Entry{{At: 1, Line: "x"}}}})
	if _, has := stream["tier"]; has || len(stream) != 3 {
		t.Errorf("stream = %v", stream)
	}
}

// A refusal other than 5xx, 429 or 408 is for good (too old, too long): retrying cannot store it.
func TestLokiPushAnswers(t *testing.T) {
	for status, wantErr := range map[int]bool{http.StatusNoContent: false, http.StatusOK: false, http.StatusBadRequest: false,
		http.StatusRequestEntityTooLarge: false, http.StatusNotFound: false, http.StatusRequestTimeout: true,
		http.StatusTooManyRequests: true, http.StatusInternalServerError: true, http.StatusServiceUnavailable: true} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("entry too far behind"))
		}))
		err := NewLokiPusher(server.URL, server.Client()).Push(context.Background(), []Stream{{
			Labels: Labels{ProjectID: "proj-a", Component: "app", Container: "c"}, Entries: []Entry{{At: 1, Line: "x"}}}})
		if (err != nil) != wantErr {
			t.Errorf("status %d: err %v", status, err)
		}
		server.Close()
	}
}

func TestLokiPushUnreachableIsAnError(t *testing.T) {
	err := NewLokiPusher("http://127.0.0.1:1", http.DefaultClient).Push(context.Background(), []Stream{{
		Labels: Labels{ProjectID: "proj-a", Component: "app", Container: "c"}, Entries: []Entry{{At: 1, Line: "x"}}}})
	if err == nil {
		t.Error("no error")
	}
}
