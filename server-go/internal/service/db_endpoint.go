package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// DBEndpointService owns how a customer reaches their database from outside
// the cluster (EXC-410).
//
// A project always reaches its own database inside the cluster, over the
// CNPG read-write Service, with no public port at all — that is how an app
// hosted on this platform should connect, and Describe reports it whether or
// not a public endpoint exists. A public endpoint is opt-in and off by
// default: it is one LoadBalancer Service on a shared address at a port of
// the project's own, and turning it off deletes that Service so the port
// stops answering.
//
// Every write here follows the lifecycle-honesty rule the rest of the
// codebase keeps: the setting is recorded only after the Kubernetes object
// has been observed in the state the setting claims.

// postgresPort is the port a Postgres cluster listens on inside the cluster.
const postgresPort = 5432

// cnpgCAKey is the key the CNPG-issued cluster CA sits under in the
// operator's per-cluster CA secret. A client using sslmode=verify-full needs
// it, so the API hands it over rather than telling customers to go and find
// it themselves.
const cnpgCAKey = "ca.crt"

// ErrDBEndpointNotConfigured is returned when the platform has no endpoint
// domain configured. Rather than invent a name, the API says the platform
// offers no public database endpoints.
var ErrDBEndpointNotConfigured = errors.New("public database endpoints are not configured on this platform")

// ErrDBEndpointUnsupported is returned for a project whose deployment mode
// has no Kubernetes Service behind it to publish.
var ErrDBEndpointUnsupported = errors.New("public database endpoints are not supported for this project's deployment mode")

// ErrDBEndpointNotObserved is returned when the Service was asked for, or
// asked to go away, and was not then seen in that state. The customer's
// setting is left as it was and the same call retried converges.
var ErrDBEndpointNotObserved = errors.New("the public database endpoint was not confirmed; retry to continue")

// PublicEndpointReconciler brings a project's public database endpoint into
// line with its lifecycle state. A project that is paused, deleting,
// restoring or otherwise not servable has no Service and therefore refuses
// connections; a resume brings the Service back on the port the project
// still holds; a deletion frees that port into quarantine.
//
// The lifecycle services hold this rather than the concrete service so a
// deployment with no public endpoints configured wires nothing and is not
// failed by a step it has no business running.
type PublicEndpointReconciler interface {
	Withdraw(ctx context.Context, inst *domain.DatabaseInstance) error
	Publish(ctx context.Context, inst *domain.DatabaseInstance) error
	Release(ctx context.Context, inst *domain.DatabaseInstance) error
}

var _ PublicEndpointReconciler = (*DBEndpointService)(nil)

// DBEndpointProjects is the slice of the instance store this service needs:
// a project's namespace, lifecycle status and database identity.
type DBEndpointProjects interface {
	FindByProjectID(projectID string) (*domain.DatabaseInstance, error)
}

// DBEndpointServiceConfig wires the service.
type DBEndpointServiceConfig struct {
	Endpoints    storage.DatabaseEndpointStore
	Instances    DBEndpointProjects
	Kube         k8s.KubeClient
	DomainSuffix string
	Ports        domain.PortRange
	Quarantine   time.Duration
	SharedIPKey  string
}

// DBEndpointService is the control plane's view of one project's public
// database endpoint.
type DBEndpointService struct {
	endpoints    storage.DatabaseEndpointStore
	instances    DBEndpointProjects
	kube         k8s.KubeClient
	domainSuffix string
	ports        domain.PortRange
	quarantine   time.Duration
	sharedIPKey  string
}

func NewDBEndpointService(c DBEndpointServiceConfig) *DBEndpointService {
	return &DBEndpointService{
		endpoints:    c.Endpoints,
		instances:    c.Instances,
		kube:         c.Kube,
		domainSuffix: c.DomainSuffix,
		ports:        c.Ports,
		quarantine:   c.Quarantine,
		sharedIPKey:  c.SharedIPKey,
	}
}

// DBEndpointInternal is the in-cluster endpoint, which exists for every
// project and needs no public port. An app the platform hosts beside the
// database should use this and nothing else.
type DBEndpointInternal struct {
	Host             string
	Port             int
	ConnectionString string
	// MongoPort and MongoConnectionString are the same for a DocumentDB
	// project's gateway, which serves the MongoDB wire protocol from a
	// container beside Postgres in the same pod (EXC-409). They are zero and
	// empty for every other project.
	MongoPort             int
	MongoConnectionString string
}

// DBEndpointView is everything a customer needs to decide about, and then
// use, their database endpoint.
//
// Enabled is the customer's choice. Available is observation: whether a
// Service is answering right now, which it is not while the project is
// paused, being deleted or restoring. Both connection strings are always
// present so the customer can see exactly what requiring TLS buys them.
type DBEndpointView struct {
	ProjectID     string
	Enabled       bool
	Available     bool
	Host          string
	Port          int
	RequireTLS    bool
	Database      string
	Username      string
	Connection    DBEndpointConnectionStrings
	CACertificate string
	Internal      DBEndpointInternal
	// MongoPort is the public port a MongoDB client dials, and
	// MongoAvailable whether the gateway behind it is serving right now.
	// Both are zero and false unless the project is a DocumentDB project
	// that has opted into public access (EXC-409).
	MongoPort      int
	MongoAvailable bool
}

// DBEndpointConnectionStrings is every string a client might need for this
// project: the Postgres pair a libpq client takes, and — for a DocumentDB
// project — the Mongo pair, which spells TLS differently and names an
// identity of its own.
type DBEndpointConnectionStrings struct {
	RequireTLS     string
	AllowPlaintext string
	// MongoRequireTLS and MongoAllowPlaintext are empty for a project that
	// is not a DocumentDB project: there is nothing for a Mongo client to
	// dial, and a string that looked like there was would be a lie.
	MongoRequireTLS     string
	MongoAllowPlaintext string
}

// Describe reports the project's endpoint as it stands.
func (s *DBEndpointService) Describe(ctx context.Context, projectID string) (DBEndpointView, error) {
	inst, err := s.project(projectID)
	if err != nil {
		return DBEndpointView{}, err
	}
	endpoint, err := s.endpoints.GetDatabaseEndpoint(ctx, projectID)
	if err != nil {
		return DBEndpointView{}, err
	}
	return s.view(ctx, inst, endpoint)
}

// SetPublic turns the project's public endpoint on or off.
//
// On: a port is taken first, then — for a project that is servable right now
// — the Service is created and observed before the choice is recorded. On a
// paused project the desired state is no Service, so the choice is recorded
// and the held port waits for the resume that will publish it.
//
// Off: the Service is deleted and observed gone before the choice is
// recorded, and only then is the port freed into quarantine. Doing it the
// other way round would leave a port free to reissue while something was
// still listening on it.
func (s *DBEndpointService) SetPublic(ctx context.Context, projectID string, public bool) (DBEndpointView, error) {
	inst, err := s.project(projectID)
	if err != nil {
		return DBEndpointView{}, err
	}
	if !public {
		return s.disable(ctx, inst)
	}
	if domain.IsNotServable(inst.Status) {
		return DBEndpointView{}, errors.New(domain.NotServableReason(inst.Status))
	}
	endpoint, err := s.endpoints.AllocateDatabaseEndpointPort(
		ctx, projectID, domain.DBEndpointRolePostgres, s.ports, s.quarantine)
	if err != nil {
		return DBEndpointView{}, err
	}
	// A DocumentDB project serves two protocols and needs a port for each.
	// Both come from this one allocator, so neither can collide with the
	// other or with another tenant's.
	mongoPort := 0
	if inst.DocumentDB {
		mongo, err := s.endpoints.AllocateDatabaseEndpointPort(
			ctx, projectID, domain.DBEndpointRoleMongo, s.ports, s.quarantine)
		if err != nil {
			return DBEndpointView{}, err
		}
		mongoPort = mongo.Port
	}
	if servableNow(inst.Status) {
		if err := s.ensureObserved(ctx, inst, endpoint.Port, mongoPort); err != nil {
			return DBEndpointView{}, err
		}
	}
	endpoint, err = s.endpoints.SetDatabaseEndpointPublic(ctx, projectID, true)
	if err != nil {
		return DBEndpointView{}, err
	}
	return s.view(ctx, inst, endpoint)
}

// SetRequireTLS records the project's TLS choice. It is enforced in Postgres
// through pg_hba — hostssl only when on — never at the edge, so nothing
// between the customer and their database ever holds their credentials.
func (s *DBEndpointService) SetRequireTLS(ctx context.Context, projectID string, requireTLS bool) (DBEndpointView, error) {
	inst, err := s.project(projectID)
	if err != nil {
		return DBEndpointView{}, err
	}
	endpoint, err := s.endpoints.SetDatabaseEndpointRequireTLS(ctx, projectID, requireTLS)
	if err != nil {
		return DBEndpointView{}, err
	}
	return s.view(ctx, inst, endpoint)
}

// Withdraw removes the project's Service without touching its setting or its
// port. A project that is paused, deleting or restoring has no Service and
// therefore refuses connections, rather than publishing a port that points
// at nothing.
func (s *DBEndpointService) Withdraw(ctx context.Context, inst *domain.DatabaseInstance) error {
	if s.inert(inst) {
		return nil
	}
	return s.deleteObserved(ctx, inst)
}

// Publish brings the project's Service back on the port it still holds. A
// project that never opted in gets nothing.
func (s *DBEndpointService) Publish(ctx context.Context, inst *domain.DatabaseInstance) error {
	if s.inert(inst) {
		return nil
	}
	endpoint, err := s.endpoints.GetDatabaseEndpoint(ctx, inst.ProjectID)
	if err != nil {
		return err
	}
	if !endpoint.IsPublic() {
		return nil
	}
	mongoPort, err := s.mongoPort(ctx, inst)
	if err != nil {
		return err
	}
	return s.ensureObserved(ctx, inst, endpoint.Port, mongoPort)
}

// mongoPort is the port the project's gateway is published on, or zero for a
// project that is not a DocumentDB project or holds none.
func (s *DBEndpointService) mongoPort(ctx context.Context, inst *domain.DatabaseInstance) (int, error) {
	if !inst.DocumentDB {
		return 0, nil
	}
	mongo, err := s.endpoints.GetDatabaseEndpointForRole(ctx, inst.ProjectID, domain.DBEndpointRoleMongo)
	if err != nil {
		return 0, err
	}
	return mongo.Port, nil
}

// Release is the teardown step: the Service goes, the port goes into
// quarantine and the row goes. Idempotent, because a teardown that failed
// part way is retried from the top.
func (s *DBEndpointService) Release(ctx context.Context, inst *domain.DatabaseInstance) error {
	if s.inert(inst) {
		return nil
	}
	if err := s.deleteObserved(ctx, inst); err != nil {
		return err
	}
	if err := s.releasePorts(ctx, inst); err != nil {
		return err
	}
	return s.endpoints.DeleteDatabaseEndpoint(ctx, inst.ProjectID)
}

// disable takes the endpoint down and frees its port.
func (s *DBEndpointService) disable(ctx context.Context, inst *domain.DatabaseInstance) (DBEndpointView, error) {
	if err := s.deleteObserved(ctx, inst); err != nil {
		return DBEndpointView{}, err
	}
	if _, err := s.endpoints.SetDatabaseEndpointPublic(ctx, inst.ProjectID, false); err != nil {
		return DBEndpointView{}, err
	}
	if err := s.releasePorts(ctx, inst); err != nil {
		return DBEndpointView{}, err
	}
	endpoint, err := s.endpoints.GetDatabaseEndpoint(ctx, inst.ProjectID)
	if err != nil {
		return DBEndpointView{}, err
	}
	return s.view(ctx, inst, endpoint)
}

// releasePorts frees every port the project holds into quarantine. A
// DocumentDB project holds two; the quarantine applies to both for the same
// reason it applies to either — a client still dialling a reissued port
// arrives at a stranger's database whichever protocol it speaks.
func (s *DBEndpointService) releasePorts(ctx context.Context, inst *domain.DatabaseInstance) error {
	roles := []domain.DBEndpointRole{domain.DBEndpointRolePostgres}
	if inst.DocumentDB {
		roles = append(roles, domain.DBEndpointRoleMongo)
	}
	for _, role := range roles {
		if err := s.endpoints.ReleaseDatabaseEndpointPort(ctx, inst.ProjectID, role, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

// ensureObserved creates the project's Services and confirms they are there. A
// create that was accepted but did not produce a Service is a failure, not a
// success. A mongoPort of zero means the project has no Mongo endpoint, and
// none is created.
func (s *DBEndpointService) ensureObserved(ctx context.Context, inst *domain.DatabaseInstance, port, mongoPort int) error {
	if err := s.openIngress(ctx, inst, mongoPort); err != nil {
		return err
	}
	if err := s.ensureServiceObserved(ctx, inst, k8s.PublicDBServiceSpec{
		Name:             domain.DBEndpointServiceName(inst.ProjectID),
		Port:             port,
		ReadWriteService: inst.ProjectID + "-postgres-rw",
		SharedIPKey:      s.sharedIPKey,
		ProjectID:        inst.ProjectID,
	}); err != nil {
		return err
	}
	if mongoPort == 0 {
		return nil
	}
	// The gateway is a container in the very same pod, so this Service
	// selects the same primary and differs only in the port it forwards to.
	return s.ensureServiceObserved(ctx, inst, k8s.PublicDBServiceSpec{
		Name:             domain.DBEndpointMongoServiceName(inst.ProjectID),
		Port:             mongoPort,
		TargetPort:       config.DocumentDBGatewayPort,
		PortName:         "documentdb",
		ReadWriteService: inst.ProjectID + "-postgres-rw",
		SharedIPKey:      s.sharedIPKey,
		ProjectID:        inst.ProjectID,
	})
}

// openIngress lets outside traffic through the project's namespace isolation
// to the ports its public Services forward to.
func (s *DBEndpointService) openIngress(ctx context.Context, inst *domain.DatabaseInstance, mongoPort int) error {
	ports := []int{postgresPort}
	if mongoPort != 0 {
		ports = append(ports, config.DocumentDBGatewayPort)
	}
	if err := s.kube.EnsurePublicDBIngressPolicy(ctx, inst.Namespace, inst.ProjectID, ports); err != nil {
		return fmt.Errorf("open public database ingress: %w", err)
	}
	return nil
}

// ensureServiceObserved creates one Service and confirms it exists.
func (s *DBEndpointService) ensureServiceObserved(ctx context.Context, inst *domain.DatabaseInstance, spec k8s.PublicDBServiceSpec) error {
	if err := s.kube.EnsurePublicDBService(ctx, inst.Namespace, spec); err != nil {
		return fmt.Errorf("publish database endpoint: %w", err)
	}
	exists, err := s.kube.PublicDBServiceExists(ctx, inst.Namespace, spec.Name)
	if err != nil {
		return fmt.Errorf("confirm database endpoint: %w", err)
	}
	if !exists {
		return ErrDBEndpointNotObserved
	}
	return nil
}

// deleteObserved removes the Service and confirms it is gone, so nothing is
// recorded as unreachable while a port is still answering.
func (s *DBEndpointService) deleteObserved(ctx context.Context, inst *domain.DatabaseInstance) error {
	// Both Services go. A Mongo Service left behind would publish a port
	// whose gateway is gone, and the port could not be safely reissued while
	// it still answered.
	names := []string{domain.DBEndpointServiceName(inst.ProjectID)}
	if inst.DocumentDB {
		names = append(names, domain.DBEndpointMongoServiceName(inst.ProjectID))
	}
	for _, name := range names {
		if err := s.kube.DeletePublicDBService(ctx, inst.Namespace, name); err != nil {
			return fmt.Errorf("withdraw database endpoint: %w", err)
		}
		exists, err := s.kube.PublicDBServiceExists(ctx, inst.Namespace, name)
		if err != nil {
			return fmt.Errorf("confirm database endpoint withdrawal: %w", err)
		}
		if exists {
			return ErrDBEndpointNotObserved
		}
	}
	if err := s.kube.DeletePublicDBIngressPolicy(ctx, inst.Namespace, inst.ProjectID); err != nil {
		return fmt.Errorf("close public database ingress: %w", err)
	}
	return nil
}

// inert reports whether this service has nothing to do for a project: the
// platform offers no public endpoints, or the project is not one a
// LoadBalancer Service can front. Lifecycle callers use it so a deployment
// without endpoints configured is not failed by them.
func (s *DBEndpointService) inert(inst *domain.DatabaseInstance) bool {
	return s == nil || s.endpoints == nil || s.kube == nil ||
		s.domainSuffix == "" || inst == nil || inst.DeploymentMode != domain.ModeK8s
}

// project loads the project, refusing one that does not exist or that this
// service cannot answer for. The refusals are distinct on purpose: "the
// platform has no endpoints" and "this project cannot have one" are
// different things for a customer to read.
func (s *DBEndpointService) project(projectID string) (*domain.DatabaseInstance, error) {
	if s.domainSuffix == "" {
		return nil, ErrDBEndpointNotConfigured
	}
	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil {
		return nil, fmt.Errorf("read project %s: %w", projectID, err)
	}
	if inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	if inst.DeploymentMode != domain.ModeK8s {
		return nil, ErrDBEndpointUnsupported
	}
	return inst, nil
}

// servableNow reports whether the project is in a state that should have a
// Service answering. Anything else — paused, pausing, resuming, provisioning,
// failed, deleting, restoring — has none, so the port refuses connections
// rather than pointing at a database that is not there.
func servableNow(status string) bool {
	return status == "ACTIVE"
}

// view renders the endpoint for a caller, asking Kubernetes whether the
// Service is really there rather than trusting the stored setting.
func (s *DBEndpointService) view(ctx context.Context, inst *domain.DatabaseInstance, endpoint domain.DBEndpoint) (DBEndpointView, error) {
	host, err := domain.DBEndpointHost(inst.ProjectID, s.domainSuffix)
	if err != nil {
		return DBEndpointView{}, err
	}
	available := false
	if endpoint.IsPublic() {
		available, err = s.kube.PublicDBServiceExists(ctx, inst.Namespace, domain.DBEndpointServiceName(inst.ProjectID))
		if err != nil {
			return DBEndpointView{}, fmt.Errorf("read database endpoint state: %w", err)
		}
	}
	postgresStrings := domain.DBEndpointConnectionStrings(host, endpoint.Port, inst.Username, inst.DatabaseName)
	view := DBEndpointView{
		ProjectID:  inst.ProjectID,
		Enabled:    endpoint.PublicEnabled,
		Available:  available,
		Host:       host,
		Port:       endpoint.Port,
		RequireTLS: endpoint.RequireTLS,
		Database:   inst.DatabaseName,
		Username:   inst.Username,
		Connection: DBEndpointConnectionStrings{
			RequireTLS:     postgresStrings.RequireTLS,
			AllowPlaintext: postgresStrings.AllowPlaintext,
		},
		Internal: DBEndpointInternal{
			Host: inst.Host,
			Port: postgresPort,
			ConnectionString: domain.DBConnectionString(
				inst.Host, postgresPort, inst.Username, inst.DatabaseName, domain.SSLModePrefer),
		},
	}
	if err := s.addMongoEndpoint(ctx, inst, host, &view); err != nil {
		return DBEndpointView{}, err
	}
	if available {
		ca, err := s.clusterCA(ctx, inst)
		if err != nil {
			return DBEndpointView{}, err
		}
		view.CACertificate = ca
	}
	return view, nil
}

// addMongoEndpoint fills in the Mongo half for a DocumentDB project, and
// leaves every Mongo field zero for any other.
//
// The internal address is reported whether or not the project publishes: an
// app this platform hosts beside the database reaches the gateway inside the
// cluster with no public port at all, and that is how it should connect.
//
// MongoAvailable is asked of the gateway container rather than inferred from
// the Service. The gateway starts by waiting for Postgres and then creating
// its Mongo user, so there is a real window in which the Service exists, the
// port is open and a Mongo client is still refused.
func (s *DBEndpointService) addMongoEndpoint(
	ctx context.Context, inst *domain.DatabaseInstance, host string, view *DBEndpointView,
) error {
	if !inst.DocumentDB {
		return nil
	}
	view.Internal.MongoPort = config.DocumentDBGatewayPort
	view.Internal.MongoConnectionString = domain.MongoConnectionString(
		k8s.DocumentDBServiceHost(inst.ProjectID, inst.Namespace), config.DocumentDBGatewayPort, inst.Username, true)

	port, err := s.mongoPort(ctx, inst)
	if err != nil {
		return err
	}
	if port == 0 || !view.Enabled {
		return nil
	}
	view.MongoPort = port
	mongoStrings := domain.DBEndpointMongoConnectionStrings(host, port, inst.Username)
	view.Connection.MongoRequireTLS = mongoStrings.RequireTLS
	view.Connection.MongoAllowPlaintext = mongoStrings.AllowPlaintext

	ready, err := s.kube.DocumentDBGatewayReady(ctx, inst.Namespace, inst.ProjectID+primaryPodSuffix)
	if err != nil {
		return fmt.Errorf("read the DocumentDB gateway state for %s: %w", inst.ProjectID, err)
	}
	view.MongoAvailable = view.Available && ready
	return nil
}

// clusterCA returns the PEM a client needs for sslmode=verify-full. It comes
// from the operator's per-cluster CA secret; a missing one is an error rather
// than an empty field, because a customer handed a verify-full connection
// string and no CA cannot connect at all.
func (s *DBEndpointService) clusterCA(ctx context.Context, inst *domain.DatabaseInstance) (string, error) {
	secret, err := s.kube.GetSecret(ctx, inst.Namespace, inst.ProjectID+"-postgres-ca")
	if err != nil {
		return "", fmt.Errorf("read cluster CA for %s: %w", inst.ProjectID, err)
	}
	pem, ok := secret[cnpgCAKey]
	if !ok || len(pem) == 0 {
		return "", fmt.Errorf("cluster CA for %s holds no %s", inst.ProjectID, cnpgCAKey)
	}
	return string(pem), nil
}
