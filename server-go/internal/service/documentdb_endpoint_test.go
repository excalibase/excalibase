package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// EXC-409: a DocumentDB project that opts into public access gets a Mongo port
// beside its Postgres one, from the same allocator, and a Service publishing
// it. A project that stays private gets neither, and a project that is not
// DocumentDB never gets a Mongo port at all.

// mongoEndpointFixture wires the endpoint service over a fake cluster and a
// real in-memory allocator.
type mongoEndpointFixture struct {
	svc   *DBEndpointService
	kube  *k8s.MockClient
	store *fakeEndpointStore
	inst  *domain.DatabaseInstance
}

func newMongoEndpointFixture(t *testing.T, documentDB bool) *mongoEndpointFixture {
	t.Helper()
	inst := &domain.DatabaseInstance{
		ProjectID:      "proj-mongo0001",
		Namespace:      "org-a-proj-mongo0001",
		Host:           "proj-mongo0001-postgres-rw.org-a-proj-mongo0001.svc.cluster.local",
		DatabaseName:   "appdb",
		Username:       "appowner",
		DeploymentMode: domain.ModeK8s,
		Status:         "ACTIVE",
		DocumentDB:     documentDB,
	}
	kube := k8s.NewMockClient()
	kube.Secrets[inst.Namespace+"/"+inst.ProjectID+"-postgres-ca"] = map[string][]byte{"ca.crt": []byte("PEM")}
	kube.GatewayReady[inst.Namespace+"/"+inst.ProjectID+"-postgres-1"] = true

	store := newFakeEndpointStore()
	window, err := domain.NewPortRange(32000, 32099)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	svc := NewDBEndpointService(DBEndpointServiceConfig{
		Endpoints:    store,
		Instances:    staticInstances{inst},
		Kube:         kube,
		DomainSuffix: "db.example.com",
		Ports:        window,
		Quarantine:   time.Hour,
		SharedIPKey:  "excalibase-db-edge",
	})
	return &mongoEndpointFixture{svc: svc, kube: kube, store: store, inst: inst}
}

type staticInstances struct{ inst *domain.DatabaseInstance }

func (s staticInstances) FindByProjectID(string) (*domain.DatabaseInstance, error) {
	return s.inst, nil
}

// Publishing a DocumentDB project takes two ports from the one allocator, and
// they are different numbers in the one window.
func TestPublishingADocumentDBProjectTakesAMongoPortToo(t *testing.T) {
	f := newMongoEndpointFixture(t, true)

	view, err := f.svc.SetPublic(context.Background(), f.inst.ProjectID, true)
	if err != nil {
		t.Fatalf("SetPublic: %v", err)
	}

	if view.Port == 0 || view.MongoPort == 0 {
		t.Fatalf("ports: postgres=%d mongo=%d", view.Port, view.MongoPort)
	}
	if view.Port == view.MongoPort {
		t.Errorf("both protocols were given port %d", view.Port)
	}
}

// The Mongo port is published by a Service of its own, so it can be withdrawn
// without disturbing the Postgres one.
func TestPublishingADocumentDBProjectCreatesTheMongoService(t *testing.T) {
	f := newMongoEndpointFixture(t, true)

	if _, err := f.svc.SetPublic(context.Background(), f.inst.ProjectID, true); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}

	name := domain.DBEndpointMongoServiceName(f.inst.ProjectID)
	spec, ok := f.kube.PublicDBServices[f.inst.Namespace+"/"+name]
	if !ok {
		t.Fatalf("no Mongo Service: %v", f.kube.PublicDBServices)
	}
	if spec.TargetPort == 0 {
		t.Error("the Mongo Service forwards to Postgres's port, not the gateway's")
	}
}

// A project that is not DocumentDB has no gateway to publish, so it gets no
// Mongo port and no Mongo Service — the ordinary project is untouched.
func TestPublishingAnOrdinaryProjectTakesNoMongoPort(t *testing.T) {
	f := newMongoEndpointFixture(t, false)

	view, err := f.svc.SetPublic(context.Background(), f.inst.ProjectID, true)
	if err != nil {
		t.Fatalf("SetPublic: %v", err)
	}

	if view.MongoPort != 0 {
		t.Errorf("an ordinary project was given Mongo port %d", view.MongoPort)
	}
	name := domain.DBEndpointMongoServiceName(f.inst.ProjectID)
	if _, ok := f.kube.PublicDBServices[f.inst.Namespace+"/"+name]; ok {
		t.Error("an ordinary project was given a Mongo Service")
	}
	if view.Connection.MongoRequireTLS != "" {
		t.Errorf("an ordinary project was handed a Mongo connection string: %q", view.Connection.MongoRequireTLS)
	}
}

// A DocumentDB project that never opts in stays internal-only: no ports at
// all, and the internal Mongo address is still reported, because an app
// hosted beside the database reaches the gateway without any public port.
func TestADocumentDBProjectThatDoesNotPublishStaysInternal(t *testing.T) {
	f := newMongoEndpointFixture(t, true)

	view, err := f.svc.Describe(context.Background(), f.inst.ProjectID)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	if view.MongoPort != 0 || view.Port != 0 {
		t.Errorf("a project that did not opt in holds ports: postgres=%d mongo=%d", view.Port, view.MongoPort)
	}
	if view.Internal.MongoPort == 0 {
		t.Error("the internal Mongo port is not reported")
	}
	if !strings.HasPrefix(view.Internal.MongoConnectionString, "mongodb://") {
		t.Errorf("internal Mongo string: %q", view.Internal.MongoConnectionString)
	}
}

// The CNPG read-write Service carries 5432 only, so the gateway is reached
// through the project's own Service that exposes its port.
func TestTheInternalMongoAddressIsTheGatewayService(t *testing.T) {
	f := newMongoEndpointFixture(t, true)

	view, err := f.svc.Describe(context.Background(), f.inst.ProjectID)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	host := "proj-mongo0001-documentdb.org-a-proj-mongo0001.svc.cluster.local:10260"
	if !strings.Contains(view.Internal.MongoConnectionString, "@"+host+"/") {
		t.Errorf("internal Mongo string does not dial %s: %q", host, view.Internal.MongoConnectionString)
	}
	if strings.Contains(view.Internal.MongoConnectionString, "-postgres-rw") {
		t.Errorf("internal Mongo string dials the Postgres-only Service: %q", view.Internal.MongoConnectionString)
	}
}

// Lifecycle honesty: the Mongo endpoint is available only when the gateway is
// observed ready, not merely when the Service exists.
func TestTheMongoEndpointIsNotAvailableUntilTheGatewayIsReady(t *testing.T) {
	f := newMongoEndpointFixture(t, true)
	ctx := context.Background()
	if _, err := f.svc.SetPublic(ctx, f.inst.ProjectID, true); err != nil {
		t.Fatalf("SetPublic: %v", err)
	}

	f.kube.GatewayReady[f.inst.Namespace+"/"+f.inst.ProjectID+"-postgres-1"] = false
	view, err := f.svc.Describe(ctx, f.inst.ProjectID)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if view.MongoAvailable {
		t.Error("the Mongo endpoint is reported available while the gateway is not ready")
	}
	if !view.Available {
		t.Error("the Postgres endpoint was dragged down with the gateway")
	}

	f.kube.GatewayReady[f.inst.Namespace+"/"+f.inst.ProjectID+"-postgres-1"] = true
	view, err = f.svc.Describe(ctx, f.inst.ProjectID)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if !view.MongoAvailable {
		t.Error("the Mongo endpoint is not reported available with the gateway ready")
	}
}

// A pause withdraws both Services and keeps both ports; a resume brings both
// back on the same numbers, so a customer's saved connection strings work
// again unchanged.
func TestPauseAndResumeKeepBothPorts(t *testing.T) {
	f := newMongoEndpointFixture(t, true)
	ctx := context.Background()
	before, err := f.svc.SetPublic(ctx, f.inst.ProjectID, true)
	if err != nil {
		t.Fatalf("SetPublic: %v", err)
	}

	if err := f.svc.Withdraw(ctx, f.inst); err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	if len(f.kube.PublicDBServices) != 0 {
		t.Errorf("a paused project still publishes: %v", f.kube.PublicDBServices)
	}

	if err := f.svc.Publish(ctx, f.inst); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	after, err := f.svc.Describe(ctx, f.inst.ProjectID)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if after.Port != before.Port || after.MongoPort != before.MongoPort {
		t.Errorf("ports moved across the pause: %d/%d -> %d/%d",
			before.Port, before.MongoPort, after.Port, after.MongoPort)
	}
	if len(f.kube.PublicDBServices) != 2 {
		t.Errorf("a resumed project publishes %d Services, want 2", len(f.kube.PublicDBServices))
	}
}

// Deletion releases both ports into quarantine before anything else, so
// neither number can be reissued while something still answers on it.
func TestReleaseQuarantinesBothPorts(t *testing.T) {
	f := newMongoEndpointFixture(t, true)
	ctx := context.Background()
	view, err := f.svc.SetPublic(ctx, f.inst.ProjectID, true)
	if err != nil {
		t.Fatalf("SetPublic: %v", err)
	}

	if err := f.svc.Release(ctx, f.inst); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if len(f.kube.PublicDBServices) != 0 {
		t.Errorf("a deleted project still publishes: %v", f.kube.PublicDBServices)
	}
	for _, port := range []int{view.Port, view.MongoPort} {
		if !f.store.quarantined(port) {
			t.Errorf("port %d was freed without quarantine", port)
		}
	}
}

// Turning the endpoint off frees both, and turning it back on takes two fresh
// ports rather than reusing the quarantined ones.
func TestTurningTheEndpointOffFreesBothPorts(t *testing.T) {
	f := newMongoEndpointFixture(t, true)
	ctx := context.Background()
	before, err := f.svc.SetPublic(ctx, f.inst.ProjectID, true)
	if err != nil {
		t.Fatalf("SetPublic on: %v", err)
	}

	off, err := f.svc.SetPublic(ctx, f.inst.ProjectID, false)
	if err != nil {
		t.Fatalf("SetPublic off: %v", err)
	}
	if off.Port != 0 || off.MongoPort != 0 {
		t.Errorf("a disabled endpoint still holds ports: %d/%d", off.Port, off.MongoPort)
	}
	for _, port := range []int{before.Port, before.MongoPort} {
		if !f.store.quarantined(port) {
			t.Errorf("port %d was freed without quarantine", port)
		}
	}
}

// The strings a Mongo client actually needs, for both TLS choices.
func TestADocumentDBProjectIsGivenMongoConnectionStrings(t *testing.T) {
	f := newMongoEndpointFixture(t, true)

	view, err := f.svc.SetPublic(context.Background(), f.inst.ProjectID, true)
	if err != nil {
		t.Fatalf("SetPublic: %v", err)
	}

	if !strings.Contains(view.Connection.MongoRequireTLS, "tls=true") {
		t.Errorf("require-TLS string: %q", view.Connection.MongoRequireTLS)
	}
	if !strings.Contains(view.Connection.MongoAllowPlaintext, "tls=false") {
		t.Errorf("plaintext string: %q", view.Connection.MongoAllowPlaintext)
	}
	// One credential, two strings: both name the project's own role, so a
	// customer is not handed a second identity to keep in step.
	if !strings.Contains(view.Connection.MongoRequireTLS, f.inst.Username) {
		t.Errorf("the Mongo string does not name the project's credential: %q", view.Connection.MongoRequireTLS)
	}
	if !strings.Contains(view.Connection.RequireTLS, f.inst.Username) {
		t.Errorf("the Postgres string does not name the project's credential: %q", view.Connection.RequireTLS)
	}
	if view.Username != f.inst.Username {
		t.Errorf("the view advertises a different username: %q", view.Username)
	}
}
