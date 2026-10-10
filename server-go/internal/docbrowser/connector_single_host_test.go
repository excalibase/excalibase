package docbrowser

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// On a single host (EXC-576) the gateway shares its database container's
// network namespace, so it answers at that container's name, and its CA is
// read from the gateway itself.

const singleHostContainer = "excalibase-proj-doc1-postgres"

type fakeSingleHost struct {
	ca  []byte
	err error
}

func (f fakeSingleHost) GatewayCA(context.Context, string) ([]byte, error) { return f.ca, f.err }

func singleHostConnector(t *testing.T, host fakeSingleHost) (*GatewayConnector, *[]*options.ClientOptions) {
	t.Helper()
	f := newConnectorFixture(t)
	f.projects.inst.DeploymentMode = domain.ModeDocker
	f.projects.inst.Namespace = "0123456789ab"
	f.projects.inst.Host = singleHostContainer
	c := NewGatewayConnector(GatewayConnectorConfig{Projects: f.projects, Credentials: f.vault, SingleHost: host, Timeout: 7 * time.Second})
	var dialed []*options.ClientOptions
	c.dial = func(opts *options.ClientOptions) (*mongo.Client, error) {
		dialed = append(dialed, opts)
		return mongo.Connect(opts)
	}
	t.Cleanup(c.Close)
	return c, &dialed
}

func TestConnectorDialsASingleHostGatewayAtItsDatabaseContainer(t *testing.T) {
	c, dialed := singleHostConnector(t, fakeSingleHost{ca: testCAPEM(t)})
	if _, err := c.Store(context.Background(), connProject); err != nil {
		t.Fatalf("Store: %v", err)
	}
	opts := (*dialed)[0]
	if strings.Join(opts.Hosts, ",") != singleHostContainer+":10260" {
		t.Fatalf("hosts %v", opts.Hosts)
	}
	if opts.TLSConfig == nil || opts.TLSConfig.InsecureSkipVerify || opts.TLSConfig.RootCAs == nil || opts.TLSConfig.ServerName != singleHostContainer {
		t.Fatalf("tls %+v", opts.TLSConfig)
	}
}

func TestConnectorRefusesASingleHostGatewayItCannotVerify(t *testing.T) {
	cases := map[string]struct {
		host fakeSingleHost
		want error
	}{
		"not running": {fakeSingleHost{err: ErrGatewayNotReady}, ErrGatewayNotReady},
		"unreadable":  {fakeSingleHost{err: errors.New("engine down")}, nil},
		"garbage ca":  {fakeSingleHost{ca: []byte("nope")}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c, dialed := singleHostConnector(t, tc.host)
			_, err := c.Store(context.Background(), connProject)
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || len(*dialed) != 0 {
				t.Fatalf("err %v dialled %d", err, len(*dialed))
			}
		})
	}
}
