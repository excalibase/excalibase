//go:build integration

package service

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/natsauth"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/nats-io/nats.go"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// busReadyTimeout bounds how long a test waits for the publisher to notice
// the bus came up.
const busReadyTimeout = 30 * time.Second

// natsContainerPort is the port NATS listens on inside the container.
var natsContainerPort = network.MustParsePort("4222/tcp")

// loopbackHost is the interface the reserved port is published on.
var loopbackHost = netip.MustParseAddr("127.0.0.1")

// TestPolicyPublisher_SurvivesABusThatIsNotUpYet is the EXC-414 client half:
// the control plane starts before NATS often enough (a restart, a chart
// upgrade) that a publisher which fails construction — or gives up
// reconnecting — silently stops delivering policy invalidations.
func TestPolicyPublisher_SurvivesABusThatIsNotUpYet(t *testing.T) {
	port := reserveHostPort(t)
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)

	opts, err := natsauth.ClientOptions(natsauth.PrincipalProvisioning, "", "CDC")
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	publisher, err := NewPolicyChangePublisher(url, opts...)
	if err != nil {
		t.Fatalf("publisher construction failed while the bus was down: %v", err)
	}
	t.Cleanup(publisher.Close)
	if publisher.Connected() {
		t.Fatal("publisher reports connected although no bus is running")
	}

	startNATSOnHostPort(t, port)
	waitForBus(t, publisher.Connected)

	received := subscribeToPolicyChanges(t, url)
	publisher.PublishPolicyChange(context.Background(), domain.PolicyChangeEvent{ProjectID: "proj-resilience"})

	select {
	case subject := <-received:
		if want := "policies.proj-resilience.changed"; subject != want {
			t.Errorf("event arrived on %q, want %q", subject, want)
		}
	case <-time.After(5 * time.Second):
		t.Error("no policy change event arrived after the bus came up")
	}
}

// TestPgDogNotifier_SurvivesABusThatIsNotUpYet holds the reload signal to
// the same contract, since a missed reload leaves PgDog routing on a stale
// config.
func TestPgDogNotifier_SurvivesABusThatIsNotUpYet(t *testing.T) {
	port := reserveHostPort(t)
	url := fmt.Sprintf("nats://127.0.0.1:%d", port)

	opts, err := natsauth.ClientOptions(natsauth.PrincipalProvisioning, "", "CDC")
	if err != nil {
		t.Fatalf("ClientOptions: %v", err)
	}
	notifier, err := NewPgDogNotifier(nil, url, opts...)
	if err != nil {
		t.Fatalf("notifier construction failed while the bus was down: %v", err)
	}
	t.Cleanup(notifier.Close)
	if notifier.Connected() {
		t.Fatal("notifier reports connected although no bus is running")
	}

	startNATSOnHostPort(t, port)
	waitForBus(t, notifier.Connected)
}

// reserveHostPort finds a free TCP port and gives it up again, so the NATS
// container can be published on a port the publisher already knows. The
// publisher has to be pointed at its bus before that bus exists, which a
// container-assigned random port cannot express.
func reserveHostPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release reserved port: %v", err)
	}
	return port
}

func startNATSOnHostPort(t *testing.T, port int) {
	t.Helper()
	ctx := context.Background()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "nats:2.10-alpine",
			ExposedPorts: []string{"4222/tcp"},
			WaitingFor:   wait.ForLog("Server is ready").WithStartupTimeout(60 * time.Second),
			// The publisher is pointed at its bus before the bus exists, so
			// the container has to land on the port reserved for it rather
			// than on one Docker picks.
			HostConfigModifier: func(hostConfig *container.HostConfig) {
				hostConfig.PortBindings = network.PortMap{
					natsContainerPort: []network.PortBinding{{HostIP: loopbackHost, HostPort: strconv.Itoa(port)}},
				}
			},
		},
		Started: true,
	})
	if err != nil {
		t.Fatalf("start nats on port %d: %v", port, err)
	}
	t.Cleanup(func() { _ = container.Terminate(ctx) })
}

// waitForBus polls the publisher's own health signal rather than sleeping,
// so the test asserts exactly what an operator would read off the gauge.
func waitForBus(t *testing.T, connected func() bool) {
	t.Helper()
	deadline := time.Now().Add(busReadyTimeout)
	for time.Now().Before(deadline) {
		if connected() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("publisher never reconnected after the bus came up")
}

func subscribeToPolicyChanges(t *testing.T, url string) <-chan string {
	t.Helper()
	conn, err := nats.Connect(url)
	if err != nil {
		t.Fatalf("subscriber connect: %v", err)
	}
	t.Cleanup(conn.Close)
	subjects := make(chan string, 1)
	if _, err := conn.Subscribe("policies.>", func(msg *nats.Msg) { subjects <- msg.Subject }); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := conn.Flush(); err != nil {
		t.Fatalf("subscriber flush: %v", err)
	}
	return subjects
}
