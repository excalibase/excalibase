package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/features"
)

func TestReadAddressGate_OffAdmitsOnlyAClose(t *testing.T) {
	reached := 0
	gate := readAddressGate(features.NewStatic())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
	}))
	cases := []struct {
		method, body string
		through      bool
	}{
		{http.MethodGet, "", false},
		{http.MethodPut, `{"enabled":true}`, false},
		{http.MethodPut, `{}`, false},
		{http.MethodPut, `not json`, false},
		{http.MethodPut, `{"enabled":false}`, true},
	}
	for _, tc := range cases {
		before := reached
		w := httptest.NewRecorder()
		gate.ServeHTTP(w, httptest.NewRequest(tc.method, "/api/projects/proj-1/db-read-address/", strings.NewReader(tc.body)))
		if through := reached > before; through != tc.through {
			t.Errorf("%s %s: through=%v", tc.method, tc.body, through)
		}
		if !tc.through && (w.Code != http.StatusNotFound || w.Body.String() != unmountedBody) {
			t.Errorf("%s %s: %d %q", tc.method, tc.body, w.Code, w.Body.String())
		}
	}
}

func TestReadAddressGate_OnAdmitsEverything(t *testing.T) {
	reached := false
	gate := readAddressGate(features.NewStatic(features.DBReadAddress))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached = true
	}))
	gate.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !reached {
		t.Fatal("flag on refused a read")
	}
}
