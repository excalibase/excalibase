package sitestest

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sites"
)

// MemoryHostnames is an in-memory HostnameStore with the Postgres store's refusals:
// a project claims a hostname once, and only one proven claim per hostname exists.
type MemoryHostnames struct {
	mu    sync.Mutex
	hosts map[string]sites.CustomHostname
}

func NewMemoryHostnames() *MemoryHostnames {
	return &MemoryHostnames{hosts: map[string]sites.CustomHostname{}}
}

// CloudflareID answers the stored Cloudflare id, which the API never shows.
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
		if existing.Hostname == hostname.Hostname && (existing.ProjectID == hostname.ProjectID || existing.VerifiedAt != nil) {
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

func (m *MemoryHostnames) VerifyHostname(_ context.Context, hostname *sites.CustomHostname, maxOnZone int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.hosts[hostname.ID]
	if !ok || existing.ProjectID != hostname.ProjectID || existing.SiteID != hostname.SiteID {
		return sites.ErrHostnameNotFound
	}
	if m.provenHolder(hostname) {
		return sites.ErrHostnameTaken
	}
	if m.proven() >= maxOnZone {
		return sites.ErrCustomDomainsFull
	}
	existing.VerifiedAt, existing.UpdatedAt = hostname.VerifiedAt, hostname.UpdatedAt
	m.hosts[hostname.ID] = existing
	return nil
}

func (m *MemoryHostnames) CountVerifiedHostnames(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.proven(), nil
}

func (m *MemoryHostnames) proven() int {
	count := 0
	for _, host := range m.hosts {
		if host.VerifiedAt != nil {
			count++
		}
	}
	return count
}

// provenHolder reports another row holding the hostname with a proof.
func (m *MemoryHostnames) provenHolder(hostname *sites.CustomHostname) bool {
	for id, host := range m.hosts {
		if id != hostname.ID && host.Hostname == hostname.Hostname && host.VerifiedAt != nil {
			return true
		}
	}
	return false
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

func (m *MemoryHostnames) DueHostnames(_ context.Context, activeCheckedBefore time.Time, limit int) ([]*sites.CustomHostname, error) {
	out := m.filter(func(host sites.CustomHostname) bool {
		return host.Status != sites.HostnameActive || host.CheckedAt.Before(activeCheckedBefore)
	})
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
	if hostname.VerifiedAt != nil && m.provenHolder(hostname) {
		return sites.ErrHostnameTaken
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
