package service

import (
	"context"
	"fmt"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
)

// A MySQL project's endpoint (ADR 0041): the same LoadBalancer and port
// allocator as Postgres, fronting MOCO's primary on 3306. Every platform user
// is REQUIRE SSL, so TLS is not a choice, and the server certificate comes
// from the project's own CA, handed out here so clients verify it (EXC-589).

const mysqlPort = 3306

// mysqlServiceSpec fronts the primary Service MOCO keeps pointed at the
// current primary, so a switchover moves the public port with it.
func (s *DBEndpointService) mysqlServiceSpec(inst *domain.DatabaseInstance, port int) k8s.PublicDBServiceSpec {
	return k8s.PublicDBServiceSpec{
		Name:             publicServiceName(inst),
		Port:             port,
		TargetPort:       mysqlPort,
		PortName:         "mysql",
		ReadWriteService: "moco-" + k8s.MySQLClusterName(inst.ProjectID) + "-primary",
		SharedIPKey:      s.sharedIPKey,
		ProjectID:        inst.ProjectID,
	}
}

func (s *DBEndpointService) mysqlView(ctx context.Context, inst *domain.DatabaseInstance, endpoint domain.DBEndpoint) (DBEndpointView, error) {
	view, err := s.mysqlEndpoints(ctx, inst, endpoint)
	if err != nil {
		return DBEndpointView{}, err
	}
	// Every login needs TLS, in-cluster ones too, so the CA goes with any
	// running database, published or not.
	if view.Available || servableNow(inst.Status) {
		if view.CACertificate, err = s.mysqlCA(ctx, inst); err != nil {
			return DBEndpointView{}, err
		}
	}
	return view, nil
}

// mysqlEndpoints fills in the internal address and, when offered, the public one.
func (s *DBEndpointService) mysqlEndpoints(ctx context.Context, inst *domain.DatabaseInstance, endpoint domain.DBEndpoint) (DBEndpointView, error) {
	view := DBEndpointView{
		ProjectID:     inst.ProjectID,
		PublicOffered: s.publicOffered(),
		RequireTLS:    true,
		Database:      inst.DatabaseName,
		Username:      inst.Username,
		Engine:        domain.CredentialEngineMySQL,
		Internal: DBEndpointInternal{
			Host:             inst.Host,
			Port:             mysqlPort,
			ConnectionString: mysqlEndpointString(inst.Host, mysqlPort, inst.Username, inst.DatabaseName),
		},
	}
	if !s.publicOffered() {
		return view, nil
	}
	host, err := domain.DBEndpointHost(inst.ProjectID, s.domainSuffix)
	if err != nil {
		return DBEndpointView{}, err
	}
	if endpoint.IsPublic() {
		view.Available, err = s.kube.PublicDBServiceExists(ctx, inst.Namespace, publicServiceName(inst))
		if err != nil {
			return DBEndpointView{}, fmt.Errorf("read database endpoint state: %w", err)
		}
	}
	view.Enabled, view.Host, view.Port = endpoint.PublicEnabled, host, endpoint.Port
	if endpoint.Port > 0 {
		view.Connection.RequireTLS = mysqlEndpointString(host, endpoint.Port, inst.Username, inst.DatabaseName)
	}
	return view, nil
}

// mysqlEndpointString names the user and leaves the password out, as the
// Postgres strings on this Developer-level surface do.
func mysqlEndpointString(host string, port int, user, database string) string {
	return "mysql://" + user + "@" + host + ":" + strconv.Itoa(port) + "/" + database + "?ssl-mode=" + domain.MySQLSSLMode
}

// mysqlCA is the CA cert-manager issued the project's server certificate from.
func (s *DBEndpointService) mysqlCA(ctx context.Context, inst *domain.DatabaseInstance) (string, error) {
	secret, err := s.kube.GetSecret(ctx, inst.Namespace, k8s.MySQLCASecretName(inst.ProjectID))
	if err != nil {
		return "", fmt.Errorf("read the MySQL CA of %s: %w", inst.ProjectID, err)
	}
	return k8s.MySQLServerCA(secret)
}

// publicServiceName is the project's public Service, named for its engine.
func publicServiceName(inst *domain.DatabaseInstance) string {
	if inst.DBType == domain.MySQL {
		return inst.ProjectID + "-mysql-public"
	}
	return domain.DBEndpointServiceName(inst.ProjectID)
}
