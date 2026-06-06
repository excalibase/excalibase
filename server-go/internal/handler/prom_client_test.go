package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPromClient_InstantValue_ParsesScalar(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "" {
			t.Error("expected query param")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1700000000,"0.42"]}]}}`))
	}))
	defer ts.Close()

	c := NewPromClient(ts.URL)
	v, err := c.InstantValue(context.Background(), `up`)
	if err != nil {
		t.Fatalf("InstantValue: %v", err)
	}
	if v != 0.42 {
		t.Errorf("value: got %v, want 0.42", v)
	}
}

func TestPromClient_InstantValue_EmptyResult(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer ts.Close()

	c := NewPromClient(ts.URL)
	v, err := c.InstantValue(context.Background(), `up`)
	if err != nil || v != 0 {
		t.Errorf("empty result should be 0/nil, got %v/%v", v, err)
	}
}

func TestPromClient_InstantValue_NotConfigured(t *testing.T) {
	c := NewPromClient("")
	if _, err := c.InstantValue(context.Background(), "up"); err == nil {
		t.Error("empty base URL should error")
	}
	// nil receiver is also guarded.
	var nilClient *PromClient
	if _, err := nilClient.InstantValue(context.Background(), "up"); err == nil {
		t.Error("nil client should error")
	}
}

func TestPromClient_InstantValue_StatusError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"status":"error","data":{"result":[]}}`))
	}))
	defer ts.Close()

	c := NewPromClient(ts.URL)
	v, err := c.InstantValue(context.Background(), "up")
	if err != nil || v != 0 {
		t.Errorf("status=error should yield 0/nil, got %v/%v", v, err)
	}
}
