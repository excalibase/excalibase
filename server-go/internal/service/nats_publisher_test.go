package service

import (
	"context"
	"testing"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/natsauth"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
)

// unreachableBus is a port nothing listens on. With the shared dial options
// a connection to it exists but never completes, which is exactly the state
// a publisher is in while NATS is restarting.
const unreachableBus = "nats://127.0.0.1:1"

func provisioningDialOptions(t *testing.T) []nats.Option {
	t.Helper()
	opts, err := natsauth.ClientOptions(natsauth.PrincipalProvisioning, "", "CDC")
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	return opts
}

// droppedPublishes reads the exported counter, so the test asserts the
// signal an operator alerts on rather than an internal flag.
func droppedPublishes(t *testing.T, publisher string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, family := range families {
		if family.GetName() != "excalibase_nats_publish_dropped_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "publisher" && label.GetValue() == publisher {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// TestPolicyChangePublisherCountsEventsTheBusCannotTake is the half of
// EXC-414 that keeps a silent outage visible: the handler interface returns
// nothing, so a dropped invalidation must show up on a counter.
func TestPolicyChangePublisherCountsEventsTheBusCannotTake(t *testing.T) {
	publisher, err := NewPolicyChangePublisher(unreachableBus, provisioningDialOptions(t)...)
	if err != nil {
		t.Fatalf("construction failed against an unreachable bus: %v", err)
	}
	defer publisher.Close()

	if publisher.Connected() {
		t.Fatal("publisher reports connected against an unreachable bus")
	}

	before := droppedPublishes(t, policyPublisherName)
	publisher.PublishPolicyChange(context.Background(), domain.PolicyChangeEvent{ProjectID: "proj-dropped"})
	if after := droppedPublishes(t, policyPublisherName); after != before+1 {
		t.Errorf("dropped counter = %v, want %v", after, before+1)
	}
}

// TestPolicyChangePublisherWithoutABusIsSilent: the blank-URL path is the
// local/dev publisher, which has nothing to drop and must not count one.
func TestPolicyChangePublisherWithoutABusIsSilent(t *testing.T) {
	publisher, err := NewPolicyChangePublisher("")
	if err != nil {
		t.Fatalf("NewPolicyChangePublisher: %v", err)
	}
	defer publisher.Close()

	if publisher.Connected() {
		t.Error("a publisher with no bus configured reports connected")
	}
	before := droppedPublishes(t, policyPublisherName)
	publisher.PublishPolicyChange(context.Background(), domain.PolicyChangeEvent{ProjectID: "proj-nobus"})
	if after := droppedPublishes(t, policyPublisherName); after != before {
		t.Errorf("dropped counter moved to %v for a publisher with no bus configured", after)
	}
}

// TestPgDogNotifierCountsReloadsTheBusCannotTake: a reload that never
// arrives leaves PgDog routing on a stale config, so it has to be counted.
func TestPgDogNotifierCountsReloadsTheBusCannotTake(t *testing.T) {
	store := &fakePgDogStore{}
	notifier, err := NewPgDogNotifier(store, unreachableBus, provisioningDialOptions(t)...)
	if err != nil {
		t.Fatalf("construction failed against an unreachable bus: %v", err)
	}
	defer notifier.Close()

	if notifier.Connected() {
		t.Fatal("notifier reports connected against an unreachable bus")
	}

	before := droppedPublishes(t, pgdogPublisherName)
	if err := notifier.RegisterCluster(context.Background(), "proj-dropped", "ns", "appdb", testPgDogAppRole); err != nil {
		t.Fatalf("RegisterCluster: %v", err)
	}
	if after := droppedPublishes(t, pgdogPublisherName); after != before+1 {
		t.Errorf("dropped counter = %v, want %v", after, before+1)
	}

	// Deregistering signals PgDog too, so it is the second drop.
	if err := notifier.DeregisterCluster(context.Background(), "proj-dropped"); err != nil {
		t.Fatalf("DeregisterCluster: %v", err)
	}
	if after := droppedPublishes(t, pgdogPublisherName); after != before+2 {
		t.Errorf("dropped counter = %v, want %v", after, before+2)
	}
}

// TestPublishersWithoutAConnectionReportNotConnected covers the nil-receiver
// and nil-connection guards the health signal is read through.
func TestPublishersWithoutAConnectionReportNotConnected(t *testing.T) {
	var absentPublisher *PolicyChangePublisher
	if absentPublisher.Connected() {
		t.Error("a nil policy publisher reports connected")
	}
	var absentNotifier *PgDogNotifier
	if absentNotifier.Connected() {
		t.Error("a nil pgdog notifier reports connected")
	}

	notifier, err := NewPgDogNotifier(nil, "")
	if err != nil {
		t.Fatalf("NewPgDogNotifier: %v", err)
	}
	if notifier.Connected() {
		t.Error("a notifier with no bus configured reports connected")
	}
}
