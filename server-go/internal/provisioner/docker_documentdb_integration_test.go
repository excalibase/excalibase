//go:build integration

package provisioner

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// A DocumentDB project on a real engine (EXC-576): provisioned, reached as a
// Mongo client would reach it from the host, paused, resumed and deleted.
//
// Run: go test -tags=integration ./internal/provisioner/ -run TestDockerDocumentDB -v
func TestDockerDocumentDBProjectServesMongoClientsFromTheHost(t *testing.T) {
	docker := requireDocker(t)
	defer docker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	projectID := fmt.Sprintf("it-docdb-%d", time.Now().UnixNano()%1_000_000)
	p := NewDockerPostgreSQLProvisioner(docker)
	req := domain.ProvisioningRequest{ProjectName: projectID, DBType: domain.PostgreSQL, PostgresVersion: "17", DocumentDB: true}
	result, err := p.Provision(ctx, req, config.TierConfig{Memory: "1Gi", CPU: "1", Instances: 1}, func(domain.ProvisioningStage) {})
	t.Cleanup(func() { _ = p.Deprovision(context.Background(), DatabaseContainerName(projectID), projectID) })
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}

	client := mongoFromHost(ctx, t, docker, projectID, result.Namespace, result.Password)
	items := client.Database("shop").Collection("items")
	if _, err := items.InsertOne(ctx, bson.M{"sku": "a1", "qty": 3}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	assertCount(ctx, t, items, 1)
	_ = client.Disconnect(ctx)

	if err := p.Pause(ctx, result.Namespace, projectID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if stopped, err := p.WorkloadStopped(ctx, result.Namespace, projectID); err != nil || !stopped {
		t.Fatalf("after pause stopped=%v err=%v", stopped, err)
	}
	if err := p.Resume(ctx, result.Namespace, projectID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	client = mongoFromHost(ctx, t, docker, projectID, result.Namespace, result.Password)
	assertCount(ctx, t, client.Database("shop").Collection("items"), 1)
	_ = client.Disconnect(ctx)

	if err := p.Deprovision(ctx, result.Namespace, projectID); err != nil {
		t.Fatalf("Deprovision: %v", err)
	}
	for _, name := range []string{DocumentDBGatewayName(projectID), DatabaseContainerName(projectID)} {
		if state, err := docker.ContainerState(ctx, name); err != nil || state.Found {
			t.Fatalf("%s after deprovision: %+v %v", name, state, err)
		}
	}
}

// Statements fed on stdin run to the end, and a failing one reports its error.
func TestDockerExecInContainerStdinReportsWhatPsqlSays(t *testing.T) {
	docker := requireDocker(t)
	defer docker.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	projectID := fmt.Sprintf("it-stdin-%d", time.Now().UnixNano()%1_000_000)
	p := NewDockerPostgreSQLProvisioner(docker)
	result, err := p.Provision(ctx, domain.ProvisioningRequest{ProjectName: projectID, DBType: domain.PostgreSQL, PostgresVersion: "17"},
		config.TierConfig{Memory: "512Mi", CPU: "0.5", Instances: 1}, func(domain.ProvisioningStage) {})
	t.Cleanup(func() { _ = p.Deprovision(context.Background(), DatabaseContainerName(projectID), projectID) })
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	out, err := docker.ExecInContainerStdin(ctx, result.Namespace, []string{"psql", "-U", "postgres", "-tA", "-v", "ON_ERROR_STOP=1", "-f", "-"}, "SELECT 40 + 2;\n")
	if err != nil || strings.TrimSpace(out) != "42" {
		t.Fatalf("out %q err %v", out, err)
	}
	_, err = docker.ExecInContainerStdin(ctx, result.Namespace, []string{"psql", "-U", "postgres", "-v", "ON_ERROR_STOP=1", "-f", "-"}, "CREATE ROLE postgres;\n")
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err %v, want psql's own message", err)
	}
}

func mongoFromHost(ctx context.Context, t *testing.T, docker *RealDockerClient, projectID, database, password string) *mongo.Client {
	t.Helper()
	state, err := docker.ContainerState(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	port := state.HostPorts[strconv.Itoa(config.DocumentDBGatewayPort)]
	if port == 0 {
		t.Fatalf("gateway port not published: %+v", state.HostPorts)
	}
	ca, err := DocumentDBGatewayCA(ctx, docker, projectID)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("unusable gateway CA")
	}
	client, err := mongo.Connect(options.Client().
		SetHosts([]string{net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}).
		SetDirect(true).
		SetAuth(options.Credential{AuthMechanism: "SCRAM-SHA-256", Username: "postgres", Password: password}).
		SetTLSConfig(&tls.Config{RootCAs: roots, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12}).
		SetServerSelectionTimeout(30 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func assertCount(ctx context.Context, t *testing.T, items *mongo.Collection, want int64) {
	t.Helper()
	got, err := items.CountDocuments(ctx, bson.M{})
	if err != nil || got != want {
		t.Fatalf("count %d err %v, want %d", got, err, want)
	}
}
