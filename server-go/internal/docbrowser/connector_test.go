package docbrowser

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/k8s"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const (
	connProject   = "proj-doc1"
	connNamespace = "org-a-proj-doc1"
	connHost      = "proj-doc1-postgres-rw.org-a-proj-doc1.svc.cluster.local"
	connAddress   = "gateway-a.invalid"
)

type fakeProjects struct {
	inst *domain.DatabaseInstance
	err  error
}

func (f fakeProjects) FindByProjectID(string) (*domain.DatabaseInstance, error) { return f.inst, f.err }

type fakeVault struct {
	creds map[string]map[string]string
	paths []string
}

func (f *fakeVault) Get(path string) (map[string]string, error) {
	f.paths = append(f.paths, path)
	creds, ok := f.creds[path]
	if !ok {
		return nil, errors.New("secret not found")
	}
	return creds, nil
}

type fakeCluster struct {
	secrets  map[string]map[string][]byte
	address  string
	addrErr  error
	lookups  int
	services []string
}

func (f *fakeCluster) GetSecret(_ context.Context, namespace, name string) (map[string][]byte, error) {
	secret, ok := f.secrets[namespace+"/"+name]
	if !ok {
		return nil, errors.New("secret not found")
	}
	return secret, nil
}

func (f *fakeCluster) DocumentDBGatewayAddress(_ context.Context, _, service string) (string, error) {
	f.lookups++
	f.services = append(f.services, service)
	return f.address, f.addrErr
}

func testCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test-ca"},
		NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

type connectorFixture struct {
	projects fakeProjects
	vault    *fakeVault
	cluster  *fakeCluster
	dialed   []*options.ClientOptions
}

func newConnectorFixture(t *testing.T) *connectorFixture {
	return &connectorFixture{
		projects: fakeProjects{inst: &domain.DatabaseInstance{
			ProjectID: connProject, Namespace: connNamespace, Host: connHost,
			DocumentDB: true, Status: "ACTIVE", DeploymentMode: domain.ModeK8s,
		}},
		vault: &fakeVault{creds: map[string]map[string]string{
			"projects/" + connProject + "/credentials/excalibase_app": {"username": "excalibase_app", "password": "app-secret"},
		}},
		cluster: &fakeCluster{
			address: connAddress,
			secrets: map[string]map[string][]byte{connNamespace + "/" + connProject + "-postgres-ca": {"ca.crt": testCAPEM(t)}},
		},
	}
}

func (f *connectorFixture) connector(t *testing.T) *GatewayConnector {
	c := NewGatewayConnector(GatewayConnectorConfig{
		Projects: f.projects, Credentials: f.vault, Cluster: f.cluster, Timeout: 7 * time.Second,
	})
	c.dial = func(opts *options.ClientOptions) (*mongo.Client, error) {
		f.dialed = append(f.dialed, opts)
		return mongo.Connect(opts)
	}
	t.Cleanup(c.Close)
	return c
}

func TestConnectorDialsTheGatewayAsTheAppRoleOverVerifiedTLS(t *testing.T) {
	f := newConnectorFixture(t)
	if _, err := f.connector(t).Store(context.Background(), connProject); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if len(f.dialed) != 1 {
		t.Fatalf("dialled %d times", len(f.dialed))
	}
	opts := f.dialed[0]
	if strings.Join(opts.Hosts, ",") != connAddress+":10260" {
		t.Errorf("hosts: %v", opts.Hosts)
	}
	if opts.Auth == nil || opts.Auth.Username != "excalibase_app" || opts.Auth.Password != "app-secret" || opts.Auth.AuthMechanism != "SCRAM-SHA-256" {
		t.Errorf("auth: %+v", opts.Auth)
	}
	assertVerifiedTLS(t, opts.TLSConfig)
	if opts.Timeout == nil || *opts.Timeout != 7*time.Second {
		t.Errorf("timeout: %v", opts.Timeout)
	}
	if opts.Direct == nil || !*opts.Direct {
		t.Error("the gateway must be dialled directly")
	}
	if f.cluster.services[0] != connProject+"-postgres-rw" {
		t.Errorf("rw service: %v", f.cluster.services)
	}
}

func assertVerifiedTLS(t *testing.T, cfg *tls.Config) {
	t.Helper()
	if cfg == nil {
		t.Fatal("no TLS")
	}
	if cfg.InsecureSkipVerify || cfg.RootCAs == nil {
		t.Error("the gateway's certificate is not verified against the cluster CA")
	}
	if cfg.ServerName != connHost {
		t.Errorf("server name: %q", cfg.ServerName)
	}
	if cfg.MinVersion < tls.VersionTLS12 {
		t.Errorf("min version: %x", cfg.MinVersion)
	}
}

func TestConnectorReusesTheClientUntilSomethingChanges(t *testing.T) {
	f := newConnectorFixture(t)
	c := f.connector(t)
	ctx := context.Background()

	for range 3 {
		if _, err := c.Store(ctx, connProject); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.dialed) != 1 {
		t.Fatalf("dialled %d times for an unchanged project", len(f.dialed))
	}
	f.vault.creds["projects/"+connProject+"/credentials/excalibase_app"]["password"] = "rotated"
	if _, err := c.Store(ctx, connProject); err != nil {
		t.Fatal(err)
	}
	f.cluster.address = "gateway-b.invalid"
	if _, err := c.Store(ctx, connProject); err != nil {
		t.Fatal(err)
	}
	if len(f.dialed) != 3 {
		t.Errorf("a rotated password or moved primary must redial: %d dials", len(f.dialed))
	}
	if len(c.clients) != 1 {
		t.Errorf("stale clients kept: %d", len(c.clients))
	}
}

func TestConnectorKeepsABoundedNumberOfClients(t *testing.T) {
	f := newConnectorFixture(t)
	c := f.connector(t)
	c.maxClients = 2
	for _, id := range []string{"p1", "p2", "p3"} {
		inst := *f.projects.inst
		inst.ProjectID, inst.Namespace = id, "ns-"+id
		c.projects = fakeProjects{inst: &inst}
		f.vault.creds["projects/"+id+"/credentials/excalibase_app"] = map[string]string{"username": "u", "password": "p"}
		f.cluster.secrets["ns-"+id+"/"+id+"-postgres-ca"] = map[string][]byte{"ca.crt": testCAPEM(t)}
		if _, err := c.Store(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.clients) != 2 {
		t.Errorf("clients: %d, cap 2", len(c.clients))
	}
	if _, ok := c.clients["p1"]; ok {
		t.Error("the least recently used client was kept")
	}
}

func TestConnectorRefusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(*connectorFixture)
		want   error
	}{
		"missing project":  {func(f *connectorFixture) { f.projects.inst = nil }, ErrProjectNotFound},
		"not documentdb":   {func(f *connectorFixture) { f.projects.inst.DocumentDB = false }, ErrNotDocumentDB},
		"not k8s":          {func(f *connectorFixture) { f.projects.inst.DeploymentMode = domain.ModeDocker }, ErrNotDocumentDB},
		"paused":           {func(f *connectorFixture) { f.projects.inst.Status = "PAUSED" }, ErrNotServable},
		"gateway starting": {func(f *connectorFixture) { f.cluster.addrErr = k8s.ErrDocumentDBGatewayNotReady }, ErrGatewayNotReady},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newConnectorFixture(t)
			tc.mutate(f)
			_, err := f.connector(t).Store(context.Background(), connProject)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if len(f.dialed) != 0 {
				t.Error("dialled anyway")
			}
		})
	}
}

func TestConnectorFailsRatherThanGuessing(t *testing.T) {
	cases := map[string]func(*connectorFixture){
		"lookup error":  func(f *connectorFixture) { f.projects.err = errors.New("db down") },
		"no credential": func(f *connectorFixture) { f.vault.creds = map[string]map[string]string{} },
		"empty password": func(f *connectorFixture) {
			f.vault.creds["projects/"+connProject+"/credentials/excalibase_app"]["password"] = ""
		},
		"no host":      func(f *connectorFixture) { f.projects.inst.Host = "" },
		"no ca secret": func(f *connectorFixture) { f.cluster.secrets = map[string]map[string][]byte{} },
		"garbage ca": func(f *connectorFixture) {
			f.cluster.secrets[connNamespace+"/"+connProject+"-postgres-ca"]["ca.crt"] = []byte("nope")
		},
		"address failure": func(f *connectorFixture) { f.cluster.addrErr = errors.New("api down") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newConnectorFixture(t)
			mutate(f)
			_, err := f.connector(t).Store(context.Background(), connProject)
			if err == nil {
				t.Fatal("want a refusal")
			}
			if len(f.dialed) != 0 {
				t.Error("dialled anyway")
			}
		})
	}
}

func TestConnectorReportsADialFailure(t *testing.T) {
	f := newConnectorFixture(t)
	c := f.connector(t)
	c.dial = func(*options.ClientOptions) (*mongo.Client, error) { return nil, errors.New("bad options") }
	if _, err := c.Store(context.Background(), connProject); err == nil {
		t.Fatal("want the dial failure")
	}
}
