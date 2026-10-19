package edgefn

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// EXC-640: a runtime 400 on deploy is the function's own code, carried as
// CodeRejectedError with the runtime's message.
func TestRuntimeClientDeploy_RefusedCodeIsCodeRejectedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"index.ts must use export default (req: Request) => Response; found only named exports: invite"}`))
	}))
	defer srv.Close()

	err := NewRuntimeClient(srv.URL, "").Deploy(context.Background(), deployRequest(testFnID, "export const x = 1"))
	var rejected *CodeRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v, want CodeRejectedError", err)
	}
	want := "index.ts must use export default (req: Request) => Response; found only named exports: invite"
	if rejected.Reason != want {
		t.Errorf("reason = %q, want %q", rejected.Reason, want)
	}
}

func TestRuntimeClientDeploy_RuntimeFailureIsNotCodeRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	err := NewRuntimeClient(srv.URL, "").Deploy(context.Background(), deployRequest(testFnID, "export default () => 1"))
	var rejected *CodeRejectedError
	if err == nil || errors.As(err, &rejected) {
		t.Fatalf("err = %v, want a plain runtime failure", err)
	}
}
