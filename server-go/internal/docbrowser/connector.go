package docbrowser

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// ErrGatewayNotReady is a DocumentDB project whose gateway is not serving yet.
var ErrGatewayNotReady = errors.New("the DocumentDB gateway is not serving yet")

// ErrSingleHostUnwired is a single host's project on a connector given no way
// to read its gateway.
var ErrSingleHostUnwired = errors.New("the document browser cannot reach single-host gateways here")

const (
	// loginCredential is the document browser's own Mongo login (EXC-410);
	// the platform's Postgres roles log in by certificate and are refused here.
	loginCredential    = "docbrowser"
	authMechanism      = "SCRAM-SHA-256"
	caKey              = "ca.crt"
	defaultMaxClients  = 32
	disconnectDeadline = 5 * time.Second
	selectionTimeout   = 5 * time.Second
)

// ProjectFinder finds a project's row.
type ProjectFinder interface {
	FindByProjectID(projectID string) (*domain.DatabaseInstance, error)
}

// CredentialGetter reads a secret from the platform vault.
type CredentialGetter interface {
	Get(path string) (map[string]string, error)
}

// Cluster is what the connector asks Kubernetes: the cluster CA the gateway's
// certificate is signed by, and where the serving gateway is.
type Cluster interface {
	GetSecret(ctx context.Context, namespace, name string) (map[string][]byte, error)
	DocumentDBGatewayAddress(ctx context.Context, namespace, readWriteService string) (string, error)
}

// SingleHost reads a single host's gateway CA from the gateway (EXC-576). It
// answers ErrGatewayNotReady while the gateway is not running.
type SingleHost interface {
	GatewayCA(ctx context.Context, projectID string) ([]byte, error)
}

type GatewayConnectorConfig struct {
	Projects    ProjectFinder
	Credentials CredentialGetter
	Cluster     Cluster
	SingleHost  SingleHost
	Timeout     time.Duration
}

type cachedClient struct {
	client      *mongo.Client
	fingerprint [32]byte
	used        time.Time
}

// GatewayConnector opens a project's gateway as the document browser's login,
// verifying the gateway against the project's cluster CA. Clients are cached
// per project and replaced when the credential or the primary changes.
type GatewayConnector struct {
	projects    ProjectFinder
	credentials CredentialGetter
	cluster     Cluster
	singleHost  SingleHost
	timeout     time.Duration
	maxClients  int
	dial        func(*options.ClientOptions) (*mongo.Client, error)

	mu      sync.Mutex
	clients map[string]*cachedClient
}

func NewGatewayConnector(c GatewayConnectorConfig) *GatewayConnector {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &GatewayConnector{
		projects:    c.Projects,
		credentials: c.Credentials,
		cluster:     c.Cluster,
		singleHost:  c.SingleHost,
		timeout:     timeout,
		maxClients:  defaultMaxClients,
		dial:        func(opts *options.ClientOptions) (*mongo.Client, error) { return mongo.Connect(opts) },
		clients:     map[string]*cachedClient{},
	}
}

type gatewayTarget struct {
	inst     *domain.DatabaseInstance
	address  string
	username string
	password string
}

func (c *GatewayConnector) Store(ctx context.Context, projectID string) (Store, error) {
	target, err := c.resolve(ctx, projectID)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256([]byte(target.address + "\x00" + target.inst.Host + "\x00" + target.username + "\x00" + target.password))
	if client := c.cached(projectID, fingerprint); client != nil {
		return &mongoStore{client: client}, nil
	}
	opts, err := c.clientOptions(ctx, target)
	if err != nil {
		return nil, err
	}
	client, err := c.dial(opts)
	if err != nil {
		return nil, fmt.Errorf("open the DocumentDB gateway of %s: %w", projectID, err)
	}
	c.keep(projectID, fingerprint, client)
	return &mongoStore{client: client}, nil
}

func (c *GatewayConnector) resolve(ctx context.Context, projectID string) (gatewayTarget, error) {
	inst, err := c.project(projectID)
	if err != nil {
		return gatewayTarget{}, err
	}
	creds, err := c.credentials.Get(fmt.Sprintf("projects/%s/credentials/%s", projectID, loginCredential))
	if err != nil {
		return gatewayTarget{}, fmt.Errorf("read the %s credential of %s: %w", loginCredential, projectID, err)
	}
	if creds["username"] == "" || creds["password"] == "" {
		return gatewayTarget{}, fmt.Errorf("the %s credential of %s is incomplete", loginCredential, projectID)
	}
	address, err := c.gatewayAddress(ctx, inst)
	if err != nil {
		return gatewayTarget{}, err
	}
	return gatewayTarget{inst: inst, address: address, username: creds["username"], password: creds["password"]}, nil
}

// gatewayAddress is where the serving gateway answers. A single host's shares
// its database container's network namespace, so answers at its name.
func (c *GatewayConnector) gatewayAddress(ctx context.Context, inst *domain.DatabaseInstance) (string, error) {
	if inst.DeploymentMode == domain.ModeDocker {
		return inst.Host, nil
	}
	address, err := c.cluster.DocumentDBGatewayAddress(ctx, inst.Namespace, inst.ProjectID+"-postgres-rw")
	if errors.Is(err, k8s.ErrDocumentDBGatewayNotReady) {
		return "", ErrGatewayNotReady
	}
	if err != nil {
		return "", fmt.Errorf("find the DocumentDB gateway of %s: %w", inst.ProjectID, err)
	}
	return address, nil
}

func (c *GatewayConnector) project(projectID string) (*domain.DatabaseInstance, error) {
	inst, err := c.projects.FindByProjectID(projectID)
	if err != nil {
		return nil, fmt.Errorf("read project %s: %w", projectID, err)
	}
	if inst == nil {
		return nil, ErrProjectNotFound
	}
	if !inst.DocumentDB || (inst.DeploymentMode != domain.ModeK8s && inst.DeploymentMode != domain.ModeDocker) {
		return nil, ErrNotDocumentDB
	}
	if inst.DeploymentMode == domain.ModeDocker && c.singleHost == nil {
		return nil, ErrSingleHostUnwired
	}
	if inst.Status != "ACTIVE" {
		return nil, fmt.Errorf("%w (%s)", ErrNotServable, inst.Status)
	}
	if inst.Host == "" {
		return nil, fmt.Errorf("project %s records no database host", projectID)
	}
	return inst, nil
}

func (c *GatewayConnector) clientOptions(ctx context.Context, target gatewayTarget) (*options.ClientOptions, error) {
	roots, err := c.clusterCA(ctx, target.inst)
	if err != nil {
		return nil, err
	}
	return options.Client().
		SetHosts([]string{net.JoinHostPort(target.address, strconv.Itoa(config.DocumentDBGatewayPort))}).
		SetDirect(true).
		SetAuth(options.Credential{AuthMechanism: authMechanism, Username: target.username, Password: target.password}).
		SetTLSConfig(&tls.Config{RootCAs: roots, ServerName: target.inst.Host, MinVersion: tls.VersionTLS12}).
		SetTimeout(c.timeout).
		SetServerSelectionTimeout(selectionTimeout).
		SetConnectTimeout(selectionTimeout).
		SetMaxPoolSize(4).
		SetAppName("excalibase-studio"), nil
}

func (c *GatewayConnector) clusterCA(ctx context.Context, inst *domain.DatabaseInstance) (*x509.CertPool, error) {
	pem, err := c.gatewayCA(ctx, inst)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("the cluster CA of %s holds no usable certificate", inst.ProjectID)
	}
	return roots, nil
}

// gatewayCA is the CA the gateway's certificate is signed by: the cluster's
// on Kubernetes, the gateway's own on a single host.
func (c *GatewayConnector) gatewayCA(ctx context.Context, inst *domain.DatabaseInstance) ([]byte, error) {
	if inst.DeploymentMode == domain.ModeDocker {
		pem, err := c.singleHost.GatewayCA(ctx, inst.ProjectID)
		if err != nil && !errors.Is(err, ErrGatewayNotReady) {
			return nil, fmt.Errorf("read the gateway CA of %s: %w", inst.ProjectID, err)
		}
		return pem, err
	}
	secret, err := c.cluster.GetSecret(ctx, inst.Namespace, inst.ProjectID+"-postgres-ca")
	if err != nil {
		return nil, fmt.Errorf("read the cluster CA of %s: %w", inst.ProjectID, err)
	}
	return secret[caKey], nil
}

func (c *GatewayConnector) cached(projectID string, fingerprint [32]byte) *mongo.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.clients[projectID]
	if !ok || entry.fingerprint != fingerprint {
		return nil
	}
	entry.used = time.Now()
	return entry.client
}

func (c *GatewayConnector) keep(projectID string, fingerprint [32]byte, client *mongo.Client) {
	c.mu.Lock()
	var retired []*mongo.Client
	if old, ok := c.clients[projectID]; ok {
		retired = append(retired, old.client)
	}
	c.clients[projectID] = &cachedClient{client: client, fingerprint: fingerprint, used: time.Now()}
	for len(c.clients) > c.maxClients {
		retired = append(retired, c.evictOldest())
	}
	c.mu.Unlock()
	go disconnect(retired)
}

func (c *GatewayConnector) evictOldest() *mongo.Client {
	var oldestID string
	var oldest *cachedClient
	for id, entry := range c.clients {
		if oldest == nil || entry.used.Before(oldest.used) {
			oldestID, oldest = id, entry
		}
	}
	delete(c.clients, oldestID)
	return oldest.client
}

// Close disconnects every cached client.
func (c *GatewayConnector) Close() {
	c.mu.Lock()
	retired := make([]*mongo.Client, 0, len(c.clients))
	for id, entry := range c.clients {
		retired = append(retired, entry.client)
		delete(c.clients, id)
	}
	c.mu.Unlock()
	disconnect(retired)
}

func disconnect(clients []*mongo.Client) {
	for _, client := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), disconnectDeadline)
		if err := client.Disconnect(ctx); err != nil {
			log.Printf("docbrowser: disconnect a retired gateway client: %v", err)
		}
		cancel()
	}
}
