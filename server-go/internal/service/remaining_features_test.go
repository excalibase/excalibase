package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// --- Network Policy ---

func TestUpdateNetworkPolicy(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	mock := k8s.NewMockClient()
	store.Create(&domain.DatabaseInstance{
		ProjectID: "np-db", OrgID: "org1", Namespace: "org1-np-db",
		Status: "ACTIVE", DBType: domain.PostgreSQL,
	})

	svc := NewNetworkPolicyService(store, mock)

	err := svc.UpdateNetworkPolicy(context.Background(), "np-db", domain.NetworkConfig{
		PolicyEnabled: true,
		AllowedCIDRs:  []string{"10.0.0.0/8", "172.16.0.0/12"},
	})
	if err != nil {
		t.Fatalf("UpdateNetworkPolicy: %v", err)
	}

	// Verify NetworkPolicy was created in the namespace
	found := false
	for key := range mock.CRDs {
		if key == "org1-np-db/np-db-network-policy" {
			found = true
		}
	}
	if !found {
		t.Error("NetworkPolicy CRD not created")
	}
}

func TestUpdateNetworkPolicyNotFound(t *testing.T) {
	dir := t.TempDir()
	store, _ := storage.NewFileSystemStore(dir)
	svc := NewNetworkPolicyService(store, k8s.NewMockClient())
	err := svc.UpdateNetworkPolicy(context.Background(), "nope", domain.NetworkConfig{})
	if err == nil {
		t.Error("expected error")
	}
}

// --- Webhook ---

func TestWebhookNotification(t *testing.T) {
	received := make(chan bool, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- true
		w.WriteHeader(200)
	}))
	defer ts.Close()

	svc := NewWebhookService()
	svc.Send(ts.URL, "duke-db", "PROVISIONED", "Database is ready")

	select {
	case <-received:
		// ok
	case <-time.After(2 * time.Second):
		t.Error("webhook not received within 2s")
	}
}

func TestWebhookInvalidURL(t *testing.T) {
	svc := NewWebhookService()
	// Should not panic on invalid URL
	svc.Send("http://invalid.local:99999", "db", "EVENT", "msg")
}

// --- Provisioning History Writer ---

func TestProvisioningHistoryWriter(t *testing.T) {
	dir := t.TempDir()
	w := NewHistoryWriter(dir)

	err := w.StartAttempt(testDBName, "001")
	if err != nil {
		t.Fatalf("StartAttempt: %v", err)
	}

	w.LogStage(testDBName, "001", domain.StageValidating, "started")
	w.LogStage(testDBName, "001", domain.StageCompleted, "done")
	w.FinalizeAttempt(testDBName, "001", "SUCCESS")

	// Verify files exist
	entries := w.ListAttempts(testDBName)
	if len(entries) != 1 {
		t.Errorf("expected 1 attempt, got %d", len(entries))
	}
}
