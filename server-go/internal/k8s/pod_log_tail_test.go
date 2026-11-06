package k8s

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestPodLogTailReadsThePodsLog(t *testing.T) {
	client := &Client{clientset: fake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p-postgres-1-full-recovery-x", Namespace: "ns"},
	})}
	out, err := client.PodLogTail(context.Background(), "ns", "p-postgres-1-full-recovery-x", "full-recovery", 200)
	if err != nil {
		t.Fatalf("PodLogTail: %v", err)
	}
	if out == "" {
		t.Error("want the log the API returned")
	}
}

// The real client asks the API server for the window and the line count.
func TestPodLogSinceAsksTheAPIServerForTheWindowAndTheTail(t *testing.T) {
	var asked url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/namespaces/ns/pods/p-postgres-1/log" {
			http.NotFound(w, r)
			return
		}
		asked = r.URL.Query()
		_, _ = w.Write([]byte("recent line\n"))
	}))
	defer server.Close()
	clientset, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{clientset: clientset}

	out, err := client.PodLogSince(context.Background(), "ns", "p-postgres-1", "postgres", 50, 6*time.Hour)
	if err != nil || out != "recent line\n" {
		t.Fatalf("PodLogSince = %q, %v", out, err)
	}
	if asked.Get("sinceSeconds") != "21600" || asked.Get("tailLines") != "50" || asked.Get("container") != "postgres" {
		t.Errorf("asked %v", asked)
	}

	if _, err := client.PodLogSince(context.Background(), "ns", "p-postgres-1", "postgres", 5, 0); err != nil {
		t.Fatal(err)
	}
	if asked.Get("sinceSeconds") != "1" {
		t.Errorf("a zero window asked for %q seconds, want 1", asked.Get("sinceSeconds"))
	}
}
