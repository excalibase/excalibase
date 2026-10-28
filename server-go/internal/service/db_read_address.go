package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// DBReadAddressService is a project's read-only address (EXC-666): CNPG's
// -ro Service or MOCO's replica Service, inside the cluster and, when an
// Admin opts in, on a public port of its own beside the primary one. New
// connections reach only hot standbys or super_read_only replicas; a session
// held across a failover is not re-routed. A database with one copy has none. It wraps the endpoint
// service so the lifecycle and the primary port's close take it along.
type DBReadAddressService struct {
	inner *DBEndpointService
	store storage.DatabaseReadAddressStore
	flags features.Flags
	// closeRetry and closeWait bound how long a close waits for a racing
	// enable to finish before giving up.
	closeRetry time.Duration
	closeWait  time.Duration
}

// OperationReadAddress holds the project lease while the read port changes.
const OperationReadAddress ProjectOperation = "read address change"

var (
	// ErrReadAddressNoReplica refuses a read address on a database with one copy.
	ErrReadAddressNoReplica = errors.New("a read address needs at least one replica; this database runs one copy")
	// ErrReadAddressNeedsPublicPort refuses a public read port while the primary one is closed.
	ErrReadAddressNeedsPublicPort = errors.New("open the database's public port before its read port")
	// ErrReadAddressCopiesUnreadable is returned when the cluster's copies could not be read.
	ErrReadAddressCopiesUnreadable = errors.New("the database's copies could not be read; retry shortly")
)

var _ PublicEndpointReconciler = (*DBReadAddressService)(nil)

func NewDBReadAddressService(inner *DBEndpointService, store storage.DatabaseReadAddressStore, flags features.Flags) *DBReadAddressService {
	r := &DBReadAddressService{inner: inner, store: store, flags: flags,
		closeRetry: 500 * time.Millisecond, closeWait: 30 * time.Second}
	// Every render of the project's public Services, an allow-list change
	// included, renders the read port with the same source ranges.
	inner.alsoPublish = r.republishReader
	return r
}

// republishReader re-renders an open read port under the given allow-list.
func (r *DBReadAddressService) republishReader(ctx context.Context, inst *domain.DatabaseInstance, policy sourcePolicy) error {
	if !features.Enabled(ctx, r.flags, features.DBReadAddress) {
		return nil
	}
	reader, err := r.inner.endpoints.GetDatabaseEndpointForRole(ctx, inst.ProjectID, domain.DBEndpointRoleReader)
	if err != nil || !reader.IsPublic() {
		return err
	}
	return r.renderReader(ctx, inst, reader.Port, policy)
}

// renderReader creates or re-renders the read Service and reads its ranges back.
func (r *DBReadAddressService) renderReader(ctx context.Context, inst *domain.DatabaseInstance, port int, policy sourcePolicy) error {
	spec := r.readerSpec(inst, port)
	spec.SourceRanges, spec.NoNodePorts = policy.ranges, policy.noNodePorts
	if err := r.inner.ensureServiceObserved(ctx, inst, spec); err != nil {
		return err
	}
	if policy.enforced {
		return r.inner.confirmSourceRanges(ctx, inst.Namespace, spec)
	}
	return nil
}

// DBReadAddressInternal is the in-cluster read address an app beside the database uses.
type DBReadAddressInternal struct {
	Host             string
	Port             int
	ConnectionString string
}

// DBReadAddressView is the project's read address as it stands. Replicas is
// observed from the cluster; Reason says why there is no read address.
type DBReadAddressView struct {
	ProjectID     string
	Engine        string
	Replicas      int
	Reason        string
	SingleHost    bool
	Internal      DBReadAddressInternal
	PublicOffered bool
	PublicOpen    bool
	Enabled       bool
	Available     bool
	Host          string
	Port          int
	// ConnectionString names the user and leaves the password out, like the
	// primary address's strings on this Developer-level surface.
	ConnectionString string
}

// Describe, SetRequireTLS: the primary endpoint, unchanged.
func (r *DBReadAddressService) Describe(ctx context.Context, projectID string) (DBEndpointView, error) {
	return r.inner.Describe(ctx, projectID)
}

func (r *DBReadAddressService) SetRequireTLS(ctx context.Context, projectID string, requireTLS bool) (DBEndpointView, error) {
	return r.inner.SetRequireTLS(ctx, projectID, requireTLS)
}

// SetPublic closing the primary port closes the read port before and after:
// an enable racing the close cannot leave a read port open on its own.
func (r *DBReadAddressService) SetPublic(ctx context.Context, projectID string, public bool) (DBEndpointView, error) {
	if public || !r.inner.publicOffered() {
		return r.inner.SetPublic(ctx, projectID, public)
	}
	inst, err := r.inner.project(projectID)
	if err != nil {
		return DBEndpointView{}, err
	}
	if err := r.closeReaderLeased(ctx, inst); err != nil {
		return DBEndpointView{}, err
	}
	view, err := r.inner.SetPublic(ctx, projectID, false)
	if err != nil {
		return DBEndpointView{}, err
	}
	if err := r.closeReaderLeased(ctx, inst); err != nil {
		return DBEndpointView{}, err
	}
	return view, nil
}

// Withdraw takes the read Service down before the primary ones; its port is kept.
func (r *DBReadAddressService) Withdraw(ctx context.Context, inst *domain.DatabaseInstance) error {
	if !r.inner.inert(inst) {
		if err := r.withdrawReader(ctx, inst); err != nil {
			return err
		}
	}
	return r.inner.Withdraw(ctx, inst)
}

// Publish brings the read Service back after the primary ones, on the port it held.
func (r *DBReadAddressService) Publish(ctx context.Context, inst *domain.DatabaseInstance) error {
	if err := r.inner.Publish(ctx, inst); err != nil {
		return err
	}
	if r.inner.inert(inst) {
		return nil
	}
	if !features.Enabled(ctx, r.flags, features.DBReadAddress) {
		// A read port left from before a flag rollback is released, not republished.
		return r.closeReader(ctx, inst)
	}
	// The inner Publish rendered an open read port with the primary ones.
	return nil
}

// Release takes the read Service and port down before the primary teardown.
func (r *DBReadAddressService) Release(ctx context.Context, inst *domain.DatabaseInstance) error {
	if !r.inner.inert(inst) {
		if err := r.closeReader(ctx, inst); err != nil {
			return err
		}
	}
	return r.inner.Release(ctx, inst)
}

// ReadAddress reports the project's read address.
func (r *DBReadAddressService) ReadAddress(ctx context.Context, projectID string) (DBReadAddressView, error) {
	inst, err := r.inner.project(projectID)
	if err != nil {
		return DBReadAddressView{}, err
	}
	return r.view(ctx, inst)
}

// SetReadAddress opens or closes the project's public read port. Opening
// needs an active project with a replica and its public port open, and is
// recorded only once the Service is observed.
func (r *DBReadAddressService) SetReadAddress(ctx context.Context, projectID string, enabled bool) (DBReadAddressView, error) {
	if !r.inner.publicOffered() || r.store == nil {
		return DBReadAddressView{}, ErrDBEndpointNotConfigured
	}
	release, claimed, err := r.inner.claimer.Claim(ctx, projectID, OperationReadAddress)
	if err != nil {
		return DBReadAddressView{}, fmt.Errorf("claim project for %s: %w", OperationReadAddress, err)
	}
	if !claimed {
		return DBReadAddressView{}, fmt.Errorf("%w (%s)", ErrProjectOperationRunning, projectID)
	}
	defer release()

	inst, err := r.inner.project(projectID)
	if err != nil {
		return DBReadAddressView{}, err
	}
	if !enabled {
		if err := r.closeReader(ctx, inst); err != nil {
			return DBReadAddressView{}, err
		}
		return r.view(ctx, inst)
	}
	if err := r.open(ctx, inst); err != nil {
		return DBReadAddressView{}, err
	}
	return r.view(ctx, inst)
}

func (r *DBReadAddressService) open(ctx context.Context, inst *domain.DatabaseInstance) error {
	if inst.Status != string(domain.StatusActive) {
		return fmt.Errorf("project %s is %s; %w", inst.ProjectID, inst.Status, ErrProjectNotActive)
	}
	replicas, err := r.replicas(ctx, inst)
	if err != nil {
		return err
	}
	if replicas == 0 {
		return ErrReadAddressNoReplica
	}
	if err := r.requirePublicPort(ctx, inst); err != nil {
		return err
	}
	reader, err := r.inner.endpoints.AllocateDatabaseEndpointPort(
		ctx, inst.ProjectID, domain.DBEndpointRoleReader, r.inner.ports, r.inner.quarantine)
	if err != nil {
		return err
	}
	policy, err := r.inner.servicePolicy(ctx, inst.ProjectID)
	if err != nil {
		return err
	}
	if err := r.renderReader(ctx, inst, reader.Port, policy); err != nil {
		return errors.Join(err, r.closeReader(ctx, inst))
	}
	// The primary port may have closed meanwhile; never leave a read port open on its own.
	if err := r.requirePublicPort(ctx, inst); err != nil {
		return errors.Join(err, r.closeReader(ctx, inst))
	}
	if _, err := r.store.SetDatabaseEndpointRolePublic(ctx, inst.ProjectID, domain.DBEndpointRoleReader, true); err != nil {
		return errors.Join(err, r.deleteReaderService(ctx, inst))
	}
	return nil
}

// requirePublicPort refuses a read port unless the primary one is open and answering.
func (r *DBReadAddressService) requirePublicPort(ctx context.Context, inst *domain.DatabaseInstance) error {
	primary, err := r.inner.endpoints.GetDatabaseEndpoint(ctx, inst.ProjectID)
	if err != nil {
		return err
	}
	if !primary.IsPublic() {
		return ErrReadAddressNeedsPublicPort
	}
	exists, err := r.inner.kube.PublicDBServiceExists(ctx, inst.Namespace, publicServiceName(inst))
	if err != nil {
		return fmt.Errorf("confirm database endpoint: %w", err)
	}
	if !exists {
		return ErrReadAddressNeedsPublicPort
	}
	return nil
}

// closeReaderLeased closes the read port under the project lease, waiting a
// bounded time for an enable that holds it.
func (r *DBReadAddressService) closeReaderLeased(ctx context.Context, inst *domain.DatabaseInstance) error {
	held, err := r.holdsReader(ctx, inst.ProjectID)
	if err != nil || !held {
		return err
	}
	deadline := time.Now().Add(r.closeWait)
	for {
		release, claimed, err := r.inner.claimer.Claim(ctx, inst.ProjectID, OperationReadAddress)
		if err != nil {
			return fmt.Errorf("claim project for %s: %w", OperationReadAddress, err)
		}
		if claimed {
			defer release()
			return r.closeReader(ctx, inst)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w (%s)", ErrProjectOperationRunning, inst.ProjectID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.closeRetry):
		}
	}
}

// closeReader withdraws the read Service and quarantines its port. A project
// that never held one costs one row read and no Kubernetes call.
func (r *DBReadAddressService) closeReader(ctx context.Context, inst *domain.DatabaseInstance) error {
	held, err := r.holdsReader(ctx, inst.ProjectID)
	if err != nil || !held {
		return err
	}
	if err := r.deleteReaderService(ctx, inst); err != nil {
		return err
	}
	return r.inner.endpoints.ReleaseDatabaseEndpointPort(ctx, inst.ProjectID, domain.DBEndpointRoleReader, time.Now())
}

// withdrawReader removes the read Service and keeps its port, for a pause.
func (r *DBReadAddressService) withdrawReader(ctx context.Context, inst *domain.DatabaseInstance) error {
	held, err := r.holdsReader(ctx, inst.ProjectID)
	if err != nil || !held {
		return err
	}
	return r.deleteReaderService(ctx, inst)
}

// holdsReader reports whether the project holds a read port or a record of one.
func (r *DBReadAddressService) holdsReader(ctx context.Context, projectID string) (bool, error) {
	reader, err := r.inner.endpoints.GetDatabaseEndpointForRole(ctx, projectID, domain.DBEndpointRoleReader)
	if err != nil {
		return false, err
	}
	return reader.Port > 0 || reader.PublicEnabled, nil
}

func (r *DBReadAddressService) deleteReaderService(ctx context.Context, inst *domain.DatabaseInstance) error {
	name := domain.DBReadAddressServiceName(inst.ProjectID, inst.DBType)
	if err := r.inner.kube.DeletePublicDBService(ctx, inst.Namespace, name); err != nil {
		return fmt.Errorf("withdraw database read address: %w", err)
	}
	return r.inner.awaitServiceGone(ctx, inst.Namespace, name)
}

// readerSpec fronts the operator's replica Service: its selector, copied,
// picks replicas only and follows a switchover.
func (r *DBReadAddressService) readerSpec(inst *domain.DatabaseInstance, port int) k8s.PublicDBServiceSpec {
	spec := k8s.PublicDBServiceSpec{
		Name:             domain.DBReadAddressServiceName(inst.ProjectID, inst.DBType),
		Port:             port,
		ReadWriteService: inst.ProjectID + "-postgres-ro",
		SharedIPKey:      r.inner.sharedIPKey,
		ProjectID:        inst.ProjectID,
	}
	if inst.DBType == domain.MySQL {
		spec.ReadWriteService = "moco-" + k8s.MySQLClusterName(inst.ProjectID) + "-replica"
		spec.TargetPort, spec.PortName = mysqlPort, "mysql"
	}
	return spec
}

// replicas is the cluster's copies beside the primary, read from the cluster
// itself: a plan change moves it, the tier does not say.
func (r *DBReadAddressService) replicas(ctx context.Context, inst *domain.DatabaseInstance) (int, error) {
	gvr, name, field := k8s.CNPGClusterGVR, inst.ProjectID+postgresClusterSuffix, "instances"
	if inst.DBType == domain.MySQL {
		gvr, name, field = k8s.MOCOClusterGVR, k8s.MySQLClusterName(inst.ProjectID), "replicas"
	}
	cluster, err := r.inner.kube.GetCRD(ctx, gvr, inst.Namespace, name)
	if apierrors.IsNotFound(err) {
		return 0, nil
	}
	if err != nil || cluster == nil {
		return 0, fmt.Errorf("%w: %v", ErrReadAddressCopiesUnreadable, err)
	}
	copies, _, err := unstructured.NestedInt64(cluster.Object, "spec", field)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrReadAddressCopiesUnreadable, err)
	}
	return domain.ReplicasOf(copies), nil
}

func (r *DBReadAddressService) rows(ctx context.Context, projectID string) (domain.DBEndpoint, domain.DBEndpoint, error) {
	reader, err := r.inner.endpoints.GetDatabaseEndpointForRole(ctx, projectID, domain.DBEndpointRoleReader)
	if err != nil {
		return domain.DBEndpoint{}, domain.DBEndpoint{}, err
	}
	primary, err := r.inner.endpoints.GetDatabaseEndpoint(ctx, projectID)
	return reader, primary, err
}

func (r *DBReadAddressService) view(ctx context.Context, inst *domain.DatabaseInstance) (DBReadAddressView, error) {
	replicas, err := r.replicas(ctx, inst)
	if err != nil {
		return DBReadAddressView{}, err
	}
	reader, primary, err := r.rows(ctx, inst.ProjectID)
	if err != nil {
		return DBReadAddressView{}, err
	}
	view := DBReadAddressView{ProjectID: inst.ProjectID, Replicas: replicas, PublicOffered: r.inner.publicOffered(),
		PublicOpen: primary.IsPublic(), Enabled: reader.PublicEnabled, Port: reader.Port}
	if inst.DBType == domain.MySQL {
		view.Engine = domain.CredentialEngineMySQL
	}
	if replicas == 0 {
		view.Reason = domain.ReadAddressNoReplica
	} else {
		view.Internal = readInternal(inst, primary)
	}
	if !view.PublicOffered {
		return view, nil
	}
	if view.Host, err = domain.DBEndpointHost(inst.ProjectID, r.inner.domainSuffix); err != nil {
		return DBReadAddressView{}, err
	}
	if reader.Port > 0 {
		view.ConnectionString = readString(inst, view.Host, reader.Port, primary)
	}
	if reader.IsPublic() && replicas > 0 {
		exists, err := r.inner.kube.PublicDBServiceExists(ctx, inst.Namespace, domain.DBReadAddressServiceName(inst.ProjectID, inst.DBType))
		if err != nil {
			return DBReadAddressView{}, fmt.Errorf("read database read address state: %w", err)
		}
		view.Available = exists
	}
	return view, nil
}

func readInternal(inst *domain.DatabaseInstance, primary domain.DBEndpoint) DBReadAddressInternal {
	if inst.DBType == domain.MySQL {
		host := k8s.MySQLReplicaHost(inst.ProjectID, inst.Namespace)
		return DBReadAddressInternal{Host: host, Port: mysqlPort,
			ConnectionString: mysqlEndpointString(host, mysqlPort, inst.Username, inst.DatabaseName)}
	}
	host := fmt.Sprintf("%s-postgres-ro.%s.svc.cluster.local", inst.ProjectID, inst.Namespace)
	return DBReadAddressInternal{Host: host, Port: postgresPort,
		ConnectionString: domain.DBConnectionString(host, postgresPort, inst.Username, inst.DatabaseName, internalSSLMode(primary))}
}

func readString(inst *domain.DatabaseInstance, host string, port int, primary domain.DBEndpoint) string {
	if inst.DBType == domain.MySQL {
		return mysqlEndpointString(host, port, inst.Username, inst.DatabaseName)
	}
	return domain.DBConnectionString(host, port, inst.Username, inst.DatabaseName, primary.SSLMode())
}
