package sitestest

import (
	"context"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"github.com/google/uuid"
)

// Zone stands in for Cloudflare for SaaS: custom hostnames by id, with the
// zone's refusal of a hostname it already holds.
type Zone struct {
	mu        sync.Mutex
	hosts     map[string]cfworkers.CustomHostname
	created   []string
	reads     int
	revalidns int
	// Log records deletes in order.
	Log        []string
	FailCreate error
	FailDelete error
	FailGet    error
	// FailCreateAfterStore creates the hostname, then answers this error, as a lost answer would.
	FailCreateAfterStore error
}

func NewZone() *Zone { return &Zone{hosts: map[string]cfworkers.CustomHostname{}} }

// Seed puts a hostname on the zone as if an earlier create got through, answering its id.
func (z *Zone) Seed(hostname, status, certificate string) string {
	z.mu.Lock()
	defer z.mu.Unlock()
	id := uuid.NewString()
	z.hosts[id] = cfworkers.CustomHostname{ID: id, Hostname: hostname, Status: status, SSL: cfworkers.HostnameSSL{Status: certificate}}
	return id
}

// SetState moves the hostname's activation and certificate status.
func (z *Zone) SetState(hostname, status, certificate string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	for id, host := range z.hosts {
		if host.Hostname == hostname {
			host.Status, host.SSL.Status = status, certificate
			z.hosts[id] = host
		}
	}
}

// Created lists "hostname/method" for every create the zone took.
func (z *Zone) Created() []string {
	z.mu.Lock()
	defer z.mu.Unlock()
	return append([]string(nil), z.created...)
}

func (z *Zone) Reads() int {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.reads
}

func (z *Zone) Revalidations() int {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.revalidns
}

func (z *Zone) Holds(hostname string) bool {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.findLocked(hostname) != nil
}

func (z *Zone) findLocked(hostname string) *cfworkers.CustomHostname {
	for _, host := range z.hosts {
		if host.Hostname == hostname {
			found := host
			return &found
		}
	}
	return nil
}

func (z *Zone) CreateCustomHostname(_ context.Context, hostname, method string) (cfworkers.CustomHostname, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.FailCreate != nil {
		return cfworkers.CustomHostname{}, z.FailCreate
	}
	if z.findLocked(hostname) != nil {
		return cfworkers.CustomHostname{}, &cfworkers.APIError{Status: http.StatusConflict, Codes: []int{1406}, Message: "Duplicate custom hostname found."}
	}
	if z.FailCreateAfterStore == nil {
		z.created = append(z.created, hostname+"/"+method)
	}
	id := uuid.NewString()
	host := cfworkers.CustomHostname{
		ID: id, Hostname: hostname, Status: "pending",
		OwnershipVerification: cfworkers.OwnershipVerification{Type: "txt", Name: "_cf-custom-hostname." + hostname, Value: "own-" + id[:8]},
		SSL: cfworkers.HostnameSSL{Status: "pending_validation", Method: method,
			ValidationRecords: []cfworkers.ValidationRecord{{TXTName: "_acme-challenge." + hostname, TXTValue: "dcv-" + id[:8]}}},
	}
	z.hosts[id] = host
	if z.FailCreateAfterStore != nil {
		return cfworkers.CustomHostname{}, z.FailCreateAfterStore
	}
	return host, nil
}

// Forget drops a hostname from the zone without a trace, as a delete by hand in the dashboard would.
func (z *Zone) Forget(hostname string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	for id, host := range z.hosts {
		if host.Hostname == hostname {
			delete(z.hosts, id)
		}
	}
}

func (z *Zone) GetCustomHostname(_ context.Context, id string) (cfworkers.CustomHostname, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.reads++
	if z.FailGet != nil {
		return cfworkers.CustomHostname{}, z.FailGet
	}
	host, ok := z.hosts[id]
	if !ok {
		return cfworkers.CustomHostname{}, &cfworkers.APIError{Status: http.StatusNotFound, Message: "not found"}
	}
	return host, nil
}

func (z *Zone) RevalidateCustomHostname(ctx context.Context, id, _ string) (cfworkers.CustomHostname, error) {
	z.mu.Lock()
	z.revalidns++
	z.reads--
	z.mu.Unlock()
	return z.GetCustomHostname(ctx, id)
}

func (z *Zone) FindCustomHostname(_ context.Context, hostname string) (*cfworkers.CustomHostname, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.findLocked(hostname), nil
}

func (z *Zone) DeleteCustomHostname(_ context.Context, id string) error {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.FailDelete != nil {
		return z.FailDelete
	}
	if host, ok := z.hosts[id]; ok {
		z.Log = append(z.Log, "delete "+host.Hostname)
		delete(z.hosts, id)
	}
	return nil
}

// MemoryHostnames is an in-memory HostnameStore with the Postgres store's refusals.
type MemoryHostnames struct {
	mu    sync.Mutex
	hosts map[string]sites.CustomHostname
}

func NewMemoryHostnames() *MemoryHostnames {
	return &MemoryHostnames{hosts: map[string]sites.CustomHostname{}}
}

// CloudflareID is the zone id stored for a hostname record.
func (m *MemoryHostnames) CloudflareID(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hosts[id].CloudflareID
}

// Age moves a hostname's creation and its time without serving back by age, as one left waiting would be.
func (m *MemoryHostnames) Age(hostname string, age time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, host := range m.hosts {
		if host.Hostname == hostname {
			host.CreatedAt = host.CreatedAt.Add(-age)
			if host.InactiveSince != nil {
				since := host.InactiveSince.Add(-age)
				host.InactiveSince = &since
			}
			m.hosts[id] = host
		}
	}
}

func (m *MemoryHostnames) CreateHostname(_ context.Context, hostname *sites.CustomHostname, maxPerProject int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	held := 0
	for _, existing := range m.hosts {
		if existing.Hostname == hostname.Hostname {
			return sites.ErrHostnameTaken
		}
		if existing.ProjectID == hostname.ProjectID {
			held++
		}
	}
	if held >= maxPerProject {
		return sites.ErrHostnameLimitReached
	}
	m.hosts[hostname.ID] = *hostname
	return nil
}

func (m *MemoryHostnames) GetHostname(_ context.Context, projectID, siteID, id string) (*sites.CustomHostname, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	host, ok := m.hosts[id]
	if !ok || host.ProjectID != projectID || host.SiteID != siteID {
		return nil, nil
	}
	return &host, nil
}

func (m *MemoryHostnames) ListHostnames(_ context.Context, projectID, siteID string) ([]*sites.CustomHostname, error) {
	return m.filter(func(host sites.CustomHostname) bool { return host.ProjectID == projectID && host.SiteID == siteID }), nil
}

func (m *MemoryHostnames) ListProjectHostnames(_ context.Context, projectID string) ([]*sites.CustomHostname, error) {
	return m.filter(func(host sites.CustomHostname) bool { return host.ProjectID == projectID }), nil
}

func (m *MemoryHostnames) PendingHostnames(_ context.Context, limit int) ([]*sites.CustomHostname, error) {
	out := m.filter(func(host sites.CustomHostname) bool { return host.Status != sites.HostnameActive })
	sort.SliceStable(out, func(i, j int) bool { return out[i].CheckedAt.Before(out[j].CheckedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemoryHostnames) filter(keep func(sites.CustomHostname) bool) []*sites.CustomHostname {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*sites.CustomHostname{}
	for _, host := range m.hosts {
		if keep(host) {
			copied := host
			out = append(out, &copied)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Hostname < out[j].Hostname })
	return out
}

func (m *MemoryHostnames) UpdateHostname(_ context.Context, hostname *sites.CustomHostname) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.hosts[hostname.ID]
	if !ok || existing.ProjectID != hostname.ProjectID || existing.SiteID != hostname.SiteID {
		return sites.ErrHostnameNotFound
	}
	m.hosts[hostname.ID] = *hostname
	return nil
}

func (m *MemoryHostnames) DeleteHostname(_ context.Context, projectID, siteID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.hosts[id]
	if !ok || existing.ProjectID != projectID || existing.SiteID != siteID {
		return sites.ErrHostnameNotFound
	}
	delete(m.hosts, id)
	return nil
}

// Resolver stands in for public DNS: TXT records by name.
type Resolver struct {
	mu      sync.Mutex
	records map[string][]string
}

func NewResolver() *Resolver { return &Resolver{records: map[string][]string{}} }

// Set publishes one TXT value under name.
func (r *Resolver) Set(name, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[name] = []string{value}
}

func (r *Resolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	values, ok := r.records[name]
	if !ok {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return values, nil
}

// Projects answers which projects are scheduled for deletion.
type Projects struct {
	mu      sync.Mutex
	stopped map[string]bool
}

func NewProjects() *Projects { return &Projects{stopped: map[string]bool{}} }

// Stop schedules the project for deletion.
func (p *Projects) Stop(projectID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped[projectID] = true
}

func (p *Projects) ProjectServing(_ context.Context, projectID string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.stopped[projectID], nil
}
