package k8s

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestCappedBufferKeepsTheFirstBytesAndSwallowsTheRest(t *testing.T) {
	buffer := &cappedBuffer{limit: 5}
	for _, chunk := range []string{"abc", "defgh", "ij"} {
		if n, err := buffer.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if buffer.String() != "abcde" {
		t.Fatalf("kept %q", buffer.String())
	}
}

// An API server that refuses the exec upgrade is reported with the pod's
// command failing, never as an empty success.
func TestStreamExecInPodReportsARefusedExec(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/namespaces/ns/pods/pod/exec") || r.URL.Query().Get("container") != "mysqld" {
			t.Errorf("exec went to %s", r.URL)
		}
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer server.Close()
	config := &rest.Config{Host: server.URL}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{clientset: clientset, restConfig: config}
	var out bytes.Buffer
	err = client.StreamExecInPod(context.Background(), "ns", "pod", "mysqld", []string{"cat", "/f"}, strings.NewReader("in"), &out)
	if err == nil || out.Len() != 0 {
		t.Fatalf("a refused exec: %v, out %q", err, out.String())
	}
	if err := client.StreamExecInPod(context.Background(), "ns", "pod", "mysqld", []string{"true"}, nil, &out); err == nil {
		t.Fatal("a refused exec without input succeeded")
	}
}
