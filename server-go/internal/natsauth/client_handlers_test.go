package natsauth

import (
	"errors"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/metrics"
	"github.com/nats-io/nats.go"
)

// applyBaseOptions collects the handlers BaseOptions installs, the way
// nats.Connect would, so each one can be driven without a server.
func applyBaseOptions(t *testing.T, client string) nats.Options {
	t.Helper()
	var applied nats.Options
	for _, opt := range BaseOptions(client) {
		if err := opt(&applied); err != nil {
			t.Fatalf("applying a base option: %v", err)
		}
	}
	return applied
}

// TestBaseOptionsHandlersTrackTheConnectionState covers the reason the
// handlers exist: a connection that is silently down looks exactly like an
// idle one, so every state change has to reach the gauge an operator reads.
func TestBaseOptionsHandlersTrackTheConnectionState(t *testing.T) {
	const client = "natsauth-test-state"
	applied := applyBaseOptions(t, client)

	// A nil *nats.Conn is what the handlers tolerate here: they only ask the
	// connection for the URL to log, and that call is nil-safe.
	applied.ConnectedCB(nil)
	if !metrics.NatsConnected(client) {
		t.Error("the connect handler did not mark the client connected")
	}

	applied.DisconnectedErrCB(nil, errors.New("bus went away"))
	if metrics.NatsConnected(client) {
		t.Error("the disconnect handler did not mark the client disconnected")
	}

	applied.ReconnectedCB(nil)
	if !metrics.NatsConnected(client) {
		t.Error("the reconnect handler did not mark the client connected again")
	}

	applied.ClosedCB(nil)
	if metrics.NatsConnected(client) {
		t.Error("the closed handler left the client marked connected")
	}
}

// TestBaseOptionsErrorHandlerToleratesAMissingSubscription: nats.go reports
// some asynchronous errors without one, and the handler must still log
// rather than dereference it.
func TestBaseOptionsErrorHandlerToleratesAMissingSubscription(t *testing.T) {
	applied := applyBaseOptions(t, "natsauth-test-errors")

	applied.AsyncErrorCB(nil, nil, errors.New("slow consumer"))
	applied.AsyncErrorCB(nil, &nats.Subscription{Subject: "cdc.>"}, nats.ErrSlowConsumer)
}
