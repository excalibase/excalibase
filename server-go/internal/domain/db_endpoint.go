package domain

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"

	"github.com/excalibase/provisioning-poc/internal/publicaddr"
)

// A project's database is reachable from outside the cluster on one shared
// public IPv4 address, at a TCP port of its own (EXC-410). The customer dials
// <projectId>.<suffix>:<port>; a wildcard DNS record points every such name at
// that one address, so the hostname carries no routing — it exists so a
// certificate can be verified against a name and so the address can change
// without every customer rewriting their connection string.
//
// Public reach is opt-in and off by default. A project already reaches its own
// database inside the cluster over the CNPG read-write Service, which is how
// an app hosted on the platform should connect; a public port exists only
// when the customer asks for one, and turning it off deletes the Service so
// the port stops answering.

// ErrInvalidPortRange marks a configured public-port window that cannot be
// allocated from.
var ErrInvalidPortRange = errors.New("invalid database endpoint port range")

// ErrInvalidDBEndpointHost marks a project id or domain suffix that cannot
// form the name a customer dials.
var ErrInvalidDBEndpointHost = errors.New("invalid database endpoint host")

// minAllocatablePort keeps the window out of the privileged range and away
// from the ports well-known services answer on. Nothing in the platform binds
// a tenant port below this, and a range that dipped into it would sooner or
// later collide with the node's own listeners.
const minAllocatablePort = 1024

// maxAllocatablePort is the largest TCP port there is.
const maxAllocatablePort = 65535

// PortRange is the inclusive window public database ports are allocated from.
// It is configuration, validated once at boot, so the allocator never has to
// decide whether the numbers it was handed make sense.
type PortRange struct {
	Min int
	Max int
}

// NewPortRange validates an allocation window. A window that is inverted,
// privileged or off the end of the port space is refused rather than
// silently clamped: an operator who wrote it down wrongly must be told.
func NewPortRange(min, max int) (PortRange, error) {
	if min < minAllocatablePort {
		return PortRange{}, fmt.Errorf("%w: minimum %d is below %d", ErrInvalidPortRange, min, minAllocatablePort)
	}
	if max > maxAllocatablePort {
		return PortRange{}, fmt.Errorf("%w: maximum %d is above %d", ErrInvalidPortRange, max, maxAllocatablePort)
	}
	if max < min {
		return PortRange{}, fmt.Errorf("%w: maximum %d is below minimum %d", ErrInvalidPortRange, max, min)
	}
	return PortRange{Min: min, Max: max}, nil
}

// Size is how many ports the window holds.
func (r PortRange) Size() int {
	if r.Max < r.Min {
		return 0
	}
	return r.Max - r.Min + 1
}

// Validate reports whether this is a window the allocator may take a port
// from. A zero PortRange is not: its bounds name port 0, and Size alone would
// call that one usable port. Callers handed a window from outside the config
// package check it here rather than trusting the struct.
func (r PortRange) Validate() error {
	_, err := NewPortRange(r.Min, r.Max)
	return err
}

// Contains reports whether a port falls inside the window.
func (r PortRange) Contains(port int) bool {
	return port >= r.Min && port <= r.Max
}

// SSL modes a customer's client is told to use. verify-full is what the API
// hands out when the project requires TLS: it checks both the certificate
// chain against the cluster CA and the name the client dialled, which is the
// whole reason the endpoint has a hostname at all.
const (
	SSLModeVerifyFull = "verify-full"
	SSLModePrefer     = "prefer"
)

// DBEndpoint is a project's public database endpoint setting.
//
// PublicEnabled off is the default and means no Kubernetes Service exists, so
// nothing answers on any port. Port is the port the project holds; it is kept
// across a pause so a resume comes back on the same number, and is only
// released — into quarantine — when the project is deleted or the customer
// turns the endpoint off. RequireTLS is enforced in Postgres through pg_hba
// (hostssl only when on), never at the edge: the session is encrypted to
// Postgres itself, so nothing between the customer and their database ever
// holds their credentials.
type DBEndpoint struct {
	ProjectID     string
	PublicEnabled bool
	Port          int
	RequireTLS    bool
}

// DefaultDBEndpoint is what a project that has never touched the setting has:
// no public port, and TLS required the moment it asks for one.
func DefaultDBEndpoint(projectID string) DBEndpoint {
	return DBEndpoint{ProjectID: projectID, PublicEnabled: false, Port: 0, RequireTLS: true}
}

// IsPublic reports whether the project should have a Service answering. Both
// halves must hold: a project that asked for an endpoint but holds no port is
// not reachable, and neither is one holding a port it has turned off.
func (e DBEndpoint) IsPublic() bool {
	return e.PublicEnabled && e.Port > 0
}

// SSLMode is the mode a client should be told to connect with.
func (e DBEndpoint) SSLMode() string {
	if e.RequireTLS {
		return SSLModeVerifyFull
	}
	return SSLModePrefer
}

// dbEndpointLabel is the shape a project id must have to be the first label of
// the name a customer dials. Project ids are generated as proj-<10 lowercase
// alphanumerics>, which satisfies it.
var dbEndpointLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// DBEndpointHost returns the name a customer dials: the project id as the
// first label under the configured suffix. The suffix is checked with the
// same rules the platform applies to any other externally reachable name, so
// a suffix that only ever resolves inside the cluster is refused instead of
// being handed to a customer as a connection string that cannot work.
func DBEndpointHost(projectID, domainSuffix string) (string, error) {
	if !dbEndpointLabel.MatchString(projectID) {
		return "", fmt.Errorf("%w: %q is not a DNS label", ErrInvalidDBEndpointHost, projectID)
	}
	if domainSuffix == "" {
		return "", fmt.Errorf("%w: no domain suffix is configured", ErrInvalidDBEndpointHost)
	}
	host := projectID + "." + domainSuffix
	if err := publicaddr.ValidateHostSyntax(host); err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidDBEndpointHost, err)
	}
	return host, nil
}

// DBEndpointServiceName is the name of the project's public LoadBalancer
// Service. It sits beside the CNPG cluster's own Services in the project
// namespace and is deliberately distinct from them: CNPG owns <cluster>-rw,
// we own <cluster>-public, and deleting ours never disturbs the operator's.
func DBEndpointServiceName(projectID string) string {
	return projectID + "-postgres-public"
}

// DBEndpointConnectionStringSet is both connection strings for an endpoint —
// the one the project's current TLS setting produces and the one the other
// setting would. The API returns both so a customer can see exactly what
// turning the setting off costs them before they turn it off.
type DBEndpointConnectionStringSet struct {
	RequireTLS     string
	AllowPlaintext string
}

// DBEndpointConnectionStrings renders both choices for one endpoint.
func DBEndpointConnectionStrings(host string, port int, user, database string) DBEndpointConnectionStringSet {
	return DBEndpointConnectionStringSet{
		RequireTLS:     DBConnectionString(host, port, user, database, SSLModeVerifyFull),
		AllowPlaintext: DBConnectionString(host, port, user, database, SSLModePrefer),
	}
}

// DBConnectionString renders a libpq URI for the endpoint. It deliberately
// carries no password: the endpoint API is a Developer-level surface and the
// tenant password is handed out only by the Admin-level credentials endpoint,
// so the string a customer copies here names the role and leaves them to
// supply its secret.
func DBConnectionString(host string, port int, user, database, sslMode string) string {
	uri := url.URL{
		Scheme:   "postgresql",
		User:     url.User(user),
		Host:     host + ":" + strconv.Itoa(port),
		Path:     "/" + database,
		RawQuery: url.Values{"sslmode": []string{sslMode}}.Encode(),
	}
	return uri.String()
}
