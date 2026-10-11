package service

import (
	"bytes"
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
)

const gatewayTLSDir = "/home/documentdb/gateway/tls/"

type documentDBInPlaceFixture struct {
	adapter *DockerBackupAdapter
	docker  *recordingDocker
	ports   *heldPorts
	inst    *domain.DatabaseInstance
}

func newDocumentDBInPlaceFixture(t *testing.T) documentDBInPlaceFixture {
	t.Helper()
	adapter, store, _, uploader, records := setupDockerAdapter(t)
	adapter.SetInstanceStore(store)
	adapter.SetProjectRegistrar(&fakeRegistrar{store: store})
	docker := newRecordingDocker()
	gateway := "excalibase-dk-1-documentdb"
	docker.served = map[string][]byte{
		gateway + ":" + gatewayTLSDir + "ca.crt":  []byte("KEPT-CA"),
		gateway + ":" + gatewayTLSDir + "tls.crt": []byte("KEPT-CERT"),
		gateway + ":" + gatewayTLSDir + "tls.key": []byte("KEPT-KEY"),
	}
	adapter.SetDockerClient(docker)
	ports := &heldPorts{held: map[string]int{"dk-1": 30007, "dk-1" + mongoHeldKey: 30008}}
	adapter.SetHostPorts(ports)
	inst, _ := store.FindByProjectID("dk-1")
	inst.DocumentDB = true
	_ = store.Update(inst)
	seedDockerBackups(t, uploader, records, map[string]time.Time{"backup-old": time.Now().Add(-2 * time.Hour)})
	return documentDBInPlaceFixture{adapter: adapter, docker: docker, ports: ports, inst: inst}
}

// EXC-594: a single host's DocumentDB project restored in place keeps its
// container name, both host ports and the CA its Mongo clients saved.
func TestDockerInPlaceRestoreRebuildsADocumentDBProject(t *testing.T) {
	f := newDocumentDBInPlaceFixture(t)
	ctx := context.Background()
	target, err := f.adapter.InPlaceRecoveryTarget(ctx, f.inst, domain.RestoreRequest{})
	if err != nil {
		t.Fatalf("target: %v", err)
	}
	if err := f.adapter.ReplaceDatabase(ctx, f.inst, target); err != nil {
		t.Fatalf("replace: %v (calls %v)", err, f.docker.calls)
	}
	gatewayGone := f.docker.index("remove:excalibase-dk-1-documentdb")
	databaseGone := f.docker.index("remove:excalibase-dk-1-postgres")
	if gatewayGone < 0 || databaseGone < 0 || gatewayGone > databaseGone {
		t.Fatalf("the gateway must go before its database: %v", f.docker.calls)
	}
	published := f.docker.specs["excalibase-dk-1-postgres"].Ports
	if published["5432"] != "30007" || published["10260"] != "30008" || len(f.ports.released) != 0 {
		t.Fatalf("published %v, released %v", published, f.ports.released)
	}
	gateway := f.docker.specs["excalibase-dk-1-documentdb"]
	if gateway.NetnsOf != "excalibase-dk-1-postgres" {
		t.Fatalf("gateway %+v", gateway)
	}
	kept := false
	for _, written := range f.docker.copies {
		kept = kept || (written.container == "excalibase-dk-1-documentdb" && bytes.Contains(written.body, []byte("KEPT-CA")))
	}
	if !kept {
		t.Fatal("the rebuilt gateway does not present the project's CA")
	}
	if f.inst.Namespace != "excalibase-dk-1-postgres" || !slices.Contains(f.docker.removed, "excalibase-dk-1-postgres") {
		t.Fatalf("namespace %q removed %v", f.inst.Namespace, f.docker.removed)
	}
}

// A copy of a DocumentDB project holds two ports of its own.
func TestDockerRestorePublishesADocumentDBCopyOnItsOwnPorts(t *testing.T) {
	f := newDocumentDBInPlaceFixture(t)
	req := domain.RestoreRequest{NewProjectName: "copy", TargetProjectID: "copy"}
	if _, err := f.adapter.Restore(context.Background(), f.inst, req); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	published := f.docker.specs["excalibase-copy-postgres"].Ports
	postgres, mongo := strconv.Itoa(f.ports.held["copy"]), strconv.Itoa(f.ports.held["copy"+mongoHeldKey])
	if published["5432"] != postgres || published["10260"] != mongo || postgres == mongo || postgres == "30007" {
		t.Fatalf("copy published on %v, held %v", published, f.ports.held)
	}
}
