package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSetNatsConnectedTracksBothDirections(t *testing.T) {
	const client = "metrics-test-both-directions"

	SetNatsConnected(client, true)
	if !NatsConnected(client) {
		t.Error("gauge reports disconnected right after a connect was recorded")
	}
	if got := testutil.ToFloat64(natsConnected.WithLabelValues(client)); got != 1 {
		t.Errorf("excalibase_nats_connected = %v, want 1", got)
	}

	SetNatsConnected(client, false)
	if NatsConnected(client) {
		t.Error("gauge still reports connected after a disconnect was recorded")
	}
	if got := testutil.ToFloat64(natsConnected.WithLabelValues(client)); got != 0 {
		t.Errorf("excalibase_nats_connected = %v, want 0", got)
	}
}

// TestNatsConnectedIsFalseForAClientThatNeverReported guards the health
// check's default: an unknown client must read as down, not as up.
func TestNatsConnectedIsFalseForAClientThatNeverReported(t *testing.T) {
	if NatsConnected("metrics-test-never-reported") {
		t.Error("a client that never reported reads as connected")
	}
}

func TestCountNatsPublishDroppedCountsEveryDrop(t *testing.T) {
	const publisher = "metrics-test-dropped"

	before := testutil.ToFloat64(natsPublishDropped.WithLabelValues(publisher))
	CountNatsPublishDropped(publisher)
	CountNatsPublishDropped(publisher)

	if got := testutil.ToFloat64(natsPublishDropped.WithLabelValues(publisher)); got != before+2 {
		t.Errorf("excalibase_nats_publish_dropped_total = %v, want %v", got, before+2)
	}
}
