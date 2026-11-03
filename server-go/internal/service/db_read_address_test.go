package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// SetDatabaseEndpointRolePublic makes the endpoint fake a read-address store too.
func (f *fakeEndpointStore) SetDatabaseEndpointRolePublic(_ context.Context, projectID string, role domain.DBEndpointRole, public bool) (domain.DBEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.row(projectID, role)
	row.PublicEnabled = public
	f.rows[endpointKey(projectID, role)] = row
	return row, nil
}

var _ storage.DatabaseReadAddressStore = (*fakeEndpointStore)(nil)

type readHarness struct {
	*endpointHarness
	reader *DBReadAddressService
}

func newReadHarness(t *testing.T, copies int64, on bool) *readHarness {
	t.Helper()
	h := newEndpointHarness(t, "ACTIVE")
	setCopies(t, h.kube, endpointNamespace+"/"+endpointProject+"-postgres", "instances", copies)
	flags := features.NewStatic()
	if on {
		flags = features.NewStatic(features.DBReadAddress)
	}
	return &readHarness{endpointHarness: h, reader: NewDBReadAddressService(h.svc, h.store, flags)}
}

func setCopies(t *testing.T, kube *k8s.MockClient, key, field string, copies int64) {
	t.Helper()
	cluster, ok := kube.CRDs[key]
	if !ok {
		cluster = &unstructured.Unstructured{Object: map[string]interface{}{}}
		kube.CRDs[key] = cluster
	}
	if err := unstructured.SetNestedField(cluster.Object, copies, "spec", field); err != nil {
		t.Fatalf("set %s: %v", field, err)
	}
}

func readerServiceKey() string {
	return endpointNamespace + "/" + domain.DBReadAddressServiceName(endpointProject, domain.PostgreSQL)
}

func (h *readHarness) openPublic(t *testing.T) {
	t.Helper()
	if _, err := h.reader.SetPublic(context.Background(), endpointProject, true); err != nil {
		t.Fatalf("open public port: %v", err)
	}
}

func (h *readHarness) enable(t *testing.T) DBReadAddressView {
	t.Helper()
	view, err := h.reader.SetReadAddress(context.Background(), endpointProject, true)
	if err != nil {
		t.Fatalf("enable read address: %v", err)
	}
	return view
}

func TestReadAddress_DescribesTheReplicasInternally(t *testing.T) {
	h := newReadHarness(t, 3, true)
	view, err := h.reader.ReadAddress(context.Background(), endpointProject)
	if err != nil {
		t.Fatalf("ReadAddress: %v", err)
	}
	if view.Replicas != 2 || view.Reason != "" {
		t.Fatalf("replicas %d reason %q", view.Replicas, view.Reason)
	}
	wantHost := endpointProject + "-postgres-ro." + endpointNamespace + ".svc.cluster.local"
	if view.Internal.Host != wantHost || view.Internal.Port != 5432 {
		t.Errorf("internal %+v", view.Internal)
	}
	if !strings.Contains(view.Internal.ConnectionString, wantHost+":5432/appdb") || strings.Contains(view.Internal.ConnectionString, ":@") {
		t.Errorf("internal string %q", view.Internal.ConnectionString)
	}
	if view.Enabled || view.Available || view.Port != 0 {
		t.Errorf("a read port nobody asked for: %+v", view)
	}
}

func TestReadAddress_OneCopyHasNone(t *testing.T) {
	h := newReadHarness(t, 1, true)
	view, err := h.reader.ReadAddress(context.Background(), endpointProject)
	if err != nil {
		t.Fatalf("ReadAddress: %v", err)
	}
	if view.Replicas != 0 || view.Reason != domain.ReadAddressNoReplica || view.Internal.Host != "" {
		t.Fatalf("one copy described as %+v", view)
	}
	h.openPublic(t)
	if _, err := h.reader.SetReadAddress(context.Background(), endpointProject, true); !errors.Is(err, ErrReadAddressNoReplica) {
		t.Fatalf("enable on one copy: %v", err)
	}
	if _, ok := h.kube.PublicDBServices[readerServiceKey()]; ok {
		t.Fatal("a reader Service was created for a database with no replica")
	}
}

func TestReadAddress_NeedsThePublicPortOpen(t *testing.T) {
	h := newReadHarness(t, 3, true)
	if _, err := h.reader.SetReadAddress(context.Background(), endpointProject, true); !errors.Is(err, ErrReadAddressNeedsPublicPort) {
		t.Fatalf("enable without a public port: %v", err)
	}
	reader, _ := h.store.GetDatabaseEndpointForRole(context.Background(), endpointProject, domain.DBEndpointRoleReader)
	if reader.Port != 0 {
		t.Fatalf("a port was taken for a refused enable: %+v", reader)
	}
}

func TestReadAddress_EnablePublishesAReplicaOnlyService(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	view := h.enable(t)

	spec, ok := h.kube.PublicDBServices[readerServiceKey()]
	if !ok {
		t.Fatal("no reader Service")
	}
	if spec.ReadWriteService != endpointProject+"-postgres-ro" || spec.SharedIPKey != "excalibase-db-edge" || spec.Port != view.Port {
		t.Errorf("reader Service %+v", spec)
	}
	primary, _ := h.store.GetDatabaseEndpoint(context.Background(), endpointProject)
	if view.Port == 0 || view.Port == primary.Port {
		t.Errorf("reader port %d, primary %d", view.Port, primary.Port)
	}
	if !view.Enabled || !view.Available || view.Host != endpointProject+".db.excalibase.io" {
		t.Errorf("view %+v", view)
	}
	if !strings.Contains(view.ConnectionString, "sslmode=verify-full") {
		t.Errorf("public read string %q", view.ConnectionString)
	}
}

func TestReadAddress_EnableIsRefusedWhileNotActive(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	h.instance.Status = "PAUSED"
	if _, err := h.reader.SetReadAddress(context.Background(), endpointProject, true); !errors.Is(err, ErrProjectNotActive) {
		t.Fatalf("enable on a paused project: %v", err)
	}
}

func TestReadAddress_EnableTakesTheProjectLease(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	release, claimed, err := h.svc.claimer.Claim(context.Background(), endpointProject, OperationPause)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	defer release()
	if _, err := h.reader.SetReadAddress(context.Background(), endpointProject, true); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("enable during another operation: %v", err)
	}
}

func TestReadAddress_EnableThatIsNotObservedRecordsNothing(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	h.kube.EnsurePublicDBError = errors.New("apiserver down")
	if _, err := h.reader.SetReadAddress(context.Background(), endpointProject, true); err == nil {
		t.Fatal("an unobserved Service was reported enabled")
	}
	reader, _ := h.store.GetDatabaseEndpointForRole(context.Background(), endpointProject, domain.DBEndpointRoleReader)
	if reader.PublicEnabled {
		t.Fatal("recorded public before the Service was observed")
	}
}

func TestReadAddress_DisableRemovesTheServiceAndQuarantinesThePort(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	port := h.enable(t).Port

	view, err := h.reader.SetReadAddress(context.Background(), endpointProject, false)
	if err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, ok := h.kube.PublicDBServices[readerServiceKey()]; ok {
		t.Fatal("reader Service still there")
	}
	if !h.store.quarantined(port) || view.Enabled || view.Port != 0 {
		t.Fatalf("port %d quarantined=%v view %+v", port, h.store.quarantined(port), view)
	}
	if !h.serviceExists(t) {
		t.Fatal("closing the read address closed the primary port")
	}
}

func TestReadAddress_ClosingThePublicPortClosesTheReadPort(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	port := h.enable(t).Port

	if _, err := h.reader.SetPublic(context.Background(), endpointProject, false); err != nil {
		t.Fatalf("close public: %v", err)
	}
	if _, ok := h.kube.PublicDBServices[readerServiceKey()]; ok {
		t.Fatal("the read port outlived the public port")
	}
	if !h.store.quarantined(port) {
		t.Fatal("the read port was not quarantined")
	}
}

func TestReadAddress_PauseWithdrawsAndResumeRepublishesOnTheSamePort(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	port := h.enable(t).Port

	if err := h.reader.Withdraw(context.Background(), h.instance); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if _, ok := h.kube.PublicDBServices[readerServiceKey()]; ok {
		t.Fatal("reader Service survived a pause")
	}
	if err := h.reader.Publish(context.Background(), h.instance); err != nil {
		t.Fatalf("publish: %v", err)
	}
	spec, ok := h.kube.PublicDBServices[readerServiceKey()]
	if !ok || spec.Port != port {
		t.Fatalf("after resume: %+v (want port %d)", spec, port)
	}
}

func TestReadAddress_FlagOffNeverPublishesButStillCleansUp(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	port := h.enable(t).Port

	off := NewDBReadAddressService(h.svc, h.store, features.NewStatic())
	if err := off.Withdraw(context.Background(), h.instance); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if err := off.Publish(context.Background(), h.instance); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, ok := h.kube.PublicDBServices[readerServiceKey()]; ok {
		t.Fatal("flag off republished the read port")
	}
	if err := off.Release(context.Background(), h.instance); err != nil {
		t.Fatalf("release: %v", err)
	}
	if !h.store.quarantined(port) {
		t.Fatal("a stored read port was not released with the flag off")
	}
}

func TestReadAddress_FlagOffTouchesNothingForAProjectWithoutOne(t *testing.T) {
	h := newReadHarness(t, 3, false)
	h.openPublic(t)
	h.kube.Calls = nil
	if err := h.reader.Withdraw(context.Background(), h.instance); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	for _, call := range h.kube.Calls {
		if strings.Contains(call, "read-public") {
			t.Fatalf("flag off with no read port still called %s", call)
		}
	}
}

func TestReadAddress_ReleaseRemovesEverything(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	port := h.enable(t).Port
	if err := h.reader.Release(context.Background(), h.instance); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, ok := h.kube.PublicDBServices[readerServiceKey()]; ok || h.serviceExists(t) {
		t.Fatal("a Service survived the teardown")
	}
	if !h.store.quarantined(port) {
		t.Fatal("reader port not quarantined on teardown")
	}
}

func TestReadAddress_DroppingToOneCopyShowsNotAvailable(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	h.enable(t)
	setCopies(t, h.kube, endpointNamespace+"/"+endpointProject+"-postgres", "instances", 1)
	view, err := h.reader.ReadAddress(context.Background(), endpointProject)
	if err != nil {
		t.Fatalf("ReadAddress: %v", err)
	}
	if view.Available || view.Replicas != 0 || view.Reason != domain.ReadAddressNoReplica || !view.Enabled {
		t.Fatalf("one copy left, view %+v", view)
	}
}

func TestReadAddress_UnreadableClusterIsAnError(t *testing.T) {
	h := newReadHarness(t, 3, true)
	delete(h.kube.CRDs, endpointNamespace+"/"+endpointProject+"-postgres")
	if _, err := h.reader.ReadAddress(context.Background(), endpointProject); err == nil {
		t.Fatal("copies guessed for a cluster that could not be read")
	}
}

func TestReadAddress_MySQLReadsMOCOsReplicaService(t *testing.T) {
	f := newMySQLEndpointFixture(t)
	store := f.svc.endpoints.(*fakeEndpointStore)
	key := f.inst.Namespace + "/" + k8s.MySQLClusterName(f.inst.ProjectID)
	setCopies(t, f.kube, key, "replicas", 3)
	reader := NewDBReadAddressService(f.svc, store, features.NewStatic(features.DBReadAddress))
	if _, err := reader.SetPublic(context.Background(), f.inst.ProjectID, true); err != nil {
		t.Fatalf("open: %v", err)
	}
	view, err := reader.SetReadAddress(context.Background(), f.inst.ProjectID, true)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	spec := f.kube.PublicDBServices[f.inst.Namespace+"/"+domain.DBReadAddressServiceName(f.inst.ProjectID, domain.MySQL)]
	if spec.ReadWriteService != "moco-proj-my0001-mysql-replica" || spec.TargetPort != 3306 || spec.PortName != "mysql" {
		t.Errorf("reader Service %+v", spec)
	}
	if view.Engine != domain.CredentialEngineMySQL || view.Replicas != 2 ||
		view.Internal.Host != k8s.MySQLReplicaHost(f.inst.ProjectID, f.inst.Namespace) ||
		!strings.Contains(view.ConnectionString, "ssl-mode=VERIFY_IDENTITY") {
		t.Errorf("view %+v", view)
	}
}

func TestReadAddress_NotOfferedWithoutEndpointsDomain(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.svc.domainSuffix = ""
	view, err := h.reader.ReadAddress(context.Background(), endpointProject)
	if err != nil {
		t.Fatalf("ReadAddress: %v", err)
	}
	if view.PublicOffered || view.Internal.Host == "" {
		t.Fatalf("view %+v", view)
	}
	if _, err := h.reader.SetReadAddress(context.Background(), endpointProject, true); !errors.Is(err, ErrDBEndpointNotConfigured) {
		t.Fatalf("enable with no endpoints: %v", err)
	}
}

func TestReadAddress_SecondCloseWaitsForARacingEnable(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.reader.closeRetry = 10 * time.Millisecond
	h.reader.closeWait = 50 * time.Millisecond
	h.openPublic(t)
	h.enable(t)
	release, _, _ := h.svc.claimer.Claim(context.Background(), endpointProject, OperationReadAddress)
	defer release()
	if err := h.reader.closeReaderLeased(context.Background(), h.instance); !errors.Is(err, ErrProjectOperationRunning) {
		t.Fatalf("close while the lease is held: %v", err)
	}
}

func TestReadAddress_ClosingThePublicPortNeedsNoLeaseWithoutAReadPort(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	release, _, _ := h.svc.claimer.Claim(context.Background(), endpointProject, OperationPause)
	defer release()
	if _, err := h.reader.SetPublic(context.Background(), endpointProject, false); err != nil {
		t.Fatalf("close public during another operation: %v", err)
	}
	if h.serviceExists(t) {
		t.Fatal("the public port is still open")
	}
}

func TestReadAddress_FlagOffResumeReleasesAStoredReadPort(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	port := h.enable(t).Port
	off := NewDBReadAddressService(h.svc, h.store, features.NewStatic())
	if err := off.Withdraw(context.Background(), h.instance); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if err := off.Publish(context.Background(), h.instance); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if !h.store.quarantined(port) {
		t.Fatal("a read port from before the rollback is still held")
	}
}

func TestReadAddress_NeedsThePrimaryServiceAnswering(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	delete(h.kube.PublicDBServices, endpointNamespace+"/"+domain.DBEndpointServiceName(endpointProject))
	if _, err := h.reader.SetReadAddress(context.Background(), endpointProject, true); !errors.Is(err, ErrReadAddressNeedsPublicPort) {
		t.Fatalf("enable with the primary Service gone: %v", err)
	}
}

type failingReadStore struct{}

func (failingReadStore) SetDatabaseEndpointRolePublic(context.Context, string, domain.DBEndpointRole, bool) (domain.DBEndpoint, error) {
	return domain.DBEndpoint{}, errors.New("platform db down")
}

func TestReadAddress_AnUnrecordedEnableTakesItsServiceDown(t *testing.T) {
	h := newReadHarness(t, 3, true)
	h.openPublic(t)
	reader := NewDBReadAddressService(h.svc, failingReadStore{}, features.NewStatic(features.DBReadAddress))
	if _, err := reader.SetReadAddress(context.Background(), endpointProject, true); err == nil {
		t.Fatal("an unrecorded enable reported success")
	}
	if _, ok := h.kube.PublicDBServices[readerServiceKey()]; ok {
		t.Fatal("a read port answers that nothing records")
	}
}
