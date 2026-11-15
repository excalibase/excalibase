package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func deadlineOf(t *testing.T, req *http.Request) (time.Time, bool) {
	t.Helper()
	var at time.Time
	var ok bool
	RequestDeadline(time.Minute)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		at, ok = r.Context().Deadline()
	})).ServeHTTP(httptest.NewRecorder(), req)
	return at, ok
}

// Every database call made for a request inherits the request's context, so
// a deadline on it bounds the wait for a pooled connection.
func TestRequestDeadline_BoundsOrdinaryRequests(t *testing.T) {
	at, ok := deadlineOf(t, httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	if !ok || time.Until(at) > time.Minute {
		t.Fatalf("deadline = %v, %v; want one within a minute", at, ok)
	}
}

func TestRequestDeadline_LeavesLongLivedRequestsAlone(t *testing.T) {
	for name, req := range map[string]*http.Request{
		"event stream": func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/api/projects/p/events", nil)
			r.Header.Set("Accept", "text/event-stream")
			return r
		}(),
		"websocket": func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/ws", nil)
			r.Header.Set("Upgrade", "websocket")
			return r
		}(),
		"log follow":  httptest.NewRequest(http.MethodGet, "/api/projects/p/logs?follow=true", nil),
		"deploy wait": httptest.NewRequest(http.MethodGet, "/api/projects/p/apps/a/deploy?wait=true", nil),
		"mcp":         httptest.NewRequest(http.MethodPost, "/mcp", nil),
		"storage":     httptest.NewRequest(http.MethodPut, "/storage/v1/object/b/k", nil),
		"upload":      httptest.NewRequest(http.MethodPost, "/api/projects/p/sites/s/deploy", nil),
	} {
		if _, ok := deadlineOf(t, req); ok {
			t.Errorf("%s got a deadline", name)
		}
	}
}

func TestRequestDeadline_KeepsAnEarlierDeadline(t *testing.T) {
	early, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	at, _ := deadlineOf(t, httptest.NewRequest(http.MethodGet, "/api/x", nil).WithContext(early))
	if time.Until(at) > 2*time.Second {
		t.Fatal("the shorter caller deadline was replaced")
	}
}
