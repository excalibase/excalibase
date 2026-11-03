package handler

import (
	"context"
	"sync"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

// Handlers under test write audit entries from concurrent requests.
func TestRecordingAudit_ConcurrentWritesKeepEveryEntry(t *testing.T) {
	audit := &recordingAudit{}
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = audit.LogAudit(context.Background(), &domain.AuditEntry{})
		}()
	}
	wg.Wait()
	if len(audit.entries) != 50 {
		t.Fatalf("entries = %d", len(audit.entries))
	}
}
