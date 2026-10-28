package service

import (
	"context"
	"fmt"
	"log"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/provisioner"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// SingleHostEndpointService describes a single host's project endpoint
// (EXC-576). The host publishes the database's ports on its loopback when the
// container is created, so there is nothing to open or close: a client on the
// host dials 127.0.0.1, one elsewhere tunnels there over SSH.
type SingleHostEndpointService struct {
	instances storage.InstanceStore
	docker    provisioner.DockerClient
	gatewayCA func(ctx context.Context, projectID string) ([]byte, error)
	mysqlCA   func(ctx context.Context, container string) (string, error)
	// everyInterface is DOCKER_DB_PUBLIC: ports bound on 0.0.0.0.
	everyInterface bool
}

const singleHostLoopback = "127.0.0.1"

func NewSingleHostEndpointService(instances storage.InstanceStore, docker provisioner.DockerClient) *SingleHostEndpointService {
	return &SingleHostEndpointService{
		instances: instances,
		docker:    docker,
		gatewayCA: func(ctx context.Context, projectID string) ([]byte, error) {
			return provisioner.DocumentDBGatewayCA(ctx, docker, projectID)
		},
		mysqlCA: func(ctx context.Context, container string) (string, error) {
			return provisioner.MySQLServerCA(ctx, docker, container)
		},
	}
}

// Describe reports where the project answers on the host right now.
func (s *SingleHostEndpointService) Describe(ctx context.Context, projectID string) (DBEndpointView, error) {
	inst, err := s.project(projectID)
	if err != nil {
		return DBEndpointView{}, err
	}
	database, err := s.docker.ContainerState(ctx, inst.Namespace)
	if err != nil {
		return DBEndpointView{}, fmt.Errorf("read the database container of %s: %w", projectID, err)
	}
	if inst.DBType == domain.MySQL {
		return s.singleHostMySQLView(ctx, inst, database)
	}
	view := DBEndpointView{
		ProjectID:     inst.ProjectID,
		PublicOffered: true,
		Enabled:       true,
		SingleHost:    true,
		Host:          singleHostLoopback,
		Database:      inst.DatabaseName,
		Username:      inst.Username,
		Internal: DBEndpointInternal{
			Host:             inst.Host,
			Port:             postgresPort,
			ConnectionString: domain.DBConnectionString(inst.Host, postgresPort, inst.Username, inst.DatabaseName, domain.SSLModeDisable),
		},
	}
	if port := database.HostPorts[strconv.Itoa(postgresPort)]; database.Running && port > 0 {
		view.Available, view.Port = true, port
		view.Connection.AllowPlaintext = domain.DBConnectionString(singleHostLoopback, port, inst.Username, inst.DatabaseName, domain.SSLModeDisable)
	}
	if inst.DocumentDB {
		if err := s.addMongo(ctx, inst, database, &view); err != nil {
			return DBEndpointView{}, err
		}
	}
	return view, nil
}

// singleHostMySQLView is the loopback port mysqld publishes; every platform
// user requires TLS, so there is no plaintext string, and the CA the
// container's certificate was issued from verifies the server (EXC-589).
func (s *SingleHostEndpointService) singleHostMySQLView(ctx context.Context, inst *domain.DatabaseInstance, database provisioner.ContainerState) (DBEndpointView, error) {
	view := DBEndpointView{
		ProjectID:     inst.ProjectID,
		PublicOffered: true,
		Enabled:       true,
		SingleHost:    true,
		RequireTLS:    true,
		Host:          singleHostLoopback,
		Database:      inst.DatabaseName,
		Username:      inst.Username,
		Engine:        domain.CredentialEngineMySQL,
		Internal: DBEndpointInternal{
			Host:             inst.Host,
			Port:             mysqlPort,
			ConnectionString: mysqlEndpointString(inst.Host, mysqlPort, inst.Username, inst.DatabaseName),
		},
	}
	if port := database.HostPorts[strconv.Itoa(mysqlPort)]; database.Running && port > 0 {
		view.Available, view.Port = true, port
		view.Connection.RequireTLS = mysqlEndpointString(singleHostLoopback, port, inst.Username, inst.DatabaseName)
	}
	if !database.Running {
		return view, nil
	}
	ca, err := s.mysqlCA(ctx, inst.Namespace)
	if err != nil {
		log.Printf("ERROR: read the MySQL CA of %s: %v", inst.ProjectID, err)
		return DBEndpointView{}, ErrServerCAUnavailable
	}
	view.CACertificate = ca
	return view, nil
}

// addMongo fills in the gateway's port and the CA its certificate is signed
// by, which a Mongo client needs; Postgres itself serves no TLS here.
func (s *SingleHostEndpointService) addMongo(ctx context.Context, inst *domain.DatabaseInstance, database provisioner.ContainerState, view *DBEndpointView) error {
	view.Internal.MongoHost = inst.Host
	view.Internal.MongoPort = config.DocumentDBGatewayPort
	view.Internal.MongoConnectionString = domain.MongoConnectionString(inst.Host, config.DocumentDBGatewayPort, inst.Username, true)
	port := database.HostPorts[strconv.Itoa(config.DocumentDBGatewayPort)]
	if !database.Running || port == 0 {
		return nil
	}
	view.MongoPort = port
	view.Connection.MongoRequireTLS = domain.DBEndpointMongoConnectionString(singleHostLoopback, port, inst.Username)
	gateway, err := s.docker.ContainerState(ctx, provisioner.DocumentDBGatewayName(inst.ProjectID))
	if err != nil {
		return fmt.Errorf("read the DocumentDB gateway of %s: %w", inst.ProjectID, err)
	}
	if !gateway.Running {
		return nil
	}
	ca, err := s.gatewayCA(ctx, inst.ProjectID)
	if err != nil {
		return err
	}
	view.MongoAvailable, view.CACertificate = true, string(ca)
	return nil
}

// SetPublic is refused: the host published the ports when it created the container.
func (s *SingleHostEndpointService) SetPublic(context.Context, string, bool) (DBEndpointView, error) {
	return DBEndpointView{}, fmt.Errorf("%w: a single host publishes its database on 127.0.0.1 only; reach it over an SSH tunnel", ErrDBEndpointUnsupported)
}

// SetRequireTLS is refused: Postgres on a single host is reached on loopback only.
func (s *SingleHostEndpointService) SetRequireTLS(context.Context, string, bool) (DBEndpointView, error) {
	return DBEndpointView{}, fmt.Errorf("%w: a single host's database is reached on its loopback or over an SSH tunnel, which carries its own encryption", ErrDBEndpointUnsupported)
}

func (s *SingleHostEndpointService) project(projectID string) (*domain.DatabaseInstance, error) {
	inst, err := s.instances.FindByProjectID(projectID)
	if err != nil {
		return nil, fmt.Errorf("read project %s: %w", projectID, err)
	}
	if inst == nil {
		return nil, fmt.Errorf("project not found: %s", projectID)
	}
	if inst.DeploymentMode != domain.ModeDocker {
		return nil, ErrDBEndpointUnsupported
	}
	return inst, nil
}

// PublishesOnEveryInterface records DOCKER_DB_PUBLIC, so the allow-list says
// "any address" rather than claiming the loopback.
func (s *SingleHostEndpointService) PublishesOnEveryInterface(public bool) {
	s.everyInterface = public
}

// AllowedSources reports who reaches the database: this host only, or with
// DOCKER_DB_PUBLIC any address, neither of which a project can change.
func (s *SingleHostEndpointService) AllowedSources(_ context.Context, projectID string) (DBAllowListView, error) {
	if _, err := s.project(projectID); err != nil {
		return DBAllowListView{}, err
	}
	access := domain.DBSourceAccessHost
	if s.everyInterface {
		access = domain.DBSourceAccessAny
	}
	return DBAllowListView{ProjectID: projectID, Access: access, Max: domain.MaxDBAllowedSources, PublicEnabled: true, SingleHost: true}, nil
}

// SetAllowedSources is refused: the host, not the project, decides the bind.
func (s *SingleHostEndpointService) SetAllowedSources(context.Context, string, domain.DBSourceAllowList) (DBAllowListView, error) {
	if s.everyInterface {
		return DBAllowListView{}, fmt.Errorf("%w: this host publishes every database on all interfaces (DOCKER_DB_PUBLIC); restrict it with the host's firewall", ErrDBEndpointUnsupported)
	}
	return DBAllowListView{}, fmt.Errorf("%w: a single host publishes its database on 127.0.0.1 only; who may tunnel in is the host's SSH access", ErrDBEndpointUnsupported)
}
