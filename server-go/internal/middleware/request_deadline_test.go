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

func deadlineFor(t *testing.T, method, target string, headers map[string]string) bool {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	_, ok := deadlineOf(t, req)
	return ok
}

// Routes that wait on long work by design keep the connection's lifetime.
func TestRequestDeadline_LongRoutesAreExemptByMethodAndPattern(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/provision/p1/snapshot/export"},
		{"GET", "/api/provision/p1/snapshot/s1/download"},
		{"POST", "/api/provision/p1/backup/restore"},
		{"POST", "/api/provision/p1/upgrade"},
		{"POST", "/api/provision/p1/major-upgrade/"},
		{"POST", "/api/provision/"},
		{"POST", "/api/projects/p1/apps/a1/deploy"},
		{"POST", "/api/projects/p1/sites/s1/deploy"},
		{"GET", "/api/projects/p1/apps/a1/logs"},
		{"POST", "/mcp"},
		{"PUT", "/storage/v1/p1/object/b/k"},
	} {
		if deadlineFor(t, tc.method, tc.path, nil) {
			t.Errorf("%s %s got a deadline", tc.method, tc.path)
		}
	}
}

// An ordinary CRUD route is bounded, and nothing the client sends lifts it.
func TestRequestDeadline_OrdinaryRoutesStayBoundedWhateverTheClientSends(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		headers      map[string]string
	}{
		{"GET", "/api/provision/p1/backup/list", nil},
		{"GET", "/api/provision/p1/snapshot/", nil},
		{"GET", "/api/projects", nil},
		{"GET", "/api/provision/p1/backup/list?wait=true", nil},
		{"GET", "/api/projects?follow=true", nil},
		{"GET", "/api/projects", map[string]string{"Upgrade": "websocket"}},
		{"GET", "/api/projects", map[string]string{"Accept": "text/event-stream"}},
		{"GET", "/api/provision/p1/snapshot/export", nil}, // right path, wrong method
		{"GET", "/api/provision/p1/x/logs", nil},
	} {
		if !deadlineFor(t, tc.method, tc.path, tc.headers) {
			t.Errorf("%s %s %v escaped the deadline", tc.method, tc.path, tc.headers)
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
