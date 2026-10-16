// Package sitestest holds in-memory stand-ins for the Sites store, the CDN and
// the project CORS list, and builders for test archives.
package sitestest

import (
	"context"
	"errors"
	"github.com/excalibase/provisioning-poc/internal/sites"
	"sort"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
)

// MemoryStore is an in-memory Store with the Postgres store's refusals.
type MemoryStore struct {
	mu      sync.Mutex
	Sites   map[string]sites.Site
	Leases  map[string]time.Time
	Deploys map[string]sites.Deploy
	Now     func() time.Time
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{Sites: map[string]sites.Site{}, Leases: map[string]time.Time{}, Deploys: map[string]sites.Deploy{}, Now: time.Now}
}

func (m *MemoryStore) Create(_ context.Context, site *sites.Site, maxSites int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	held := 0
	for _, existing := range m.Sites {
		if existing.Name == site.Name {
			return sites.ErrSiteNameTaken
		}
		if existing.ProjectID == site.ProjectID {
			held++
		}
	}
	if held >= maxSites {
		return sites.ErrSiteLimitReached
	}
	m.Sites[site.ID] = *site
	return nil
}

func (m *MemoryStore) Get(_ context.Context, projectID, id string) (*sites.Site, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	site, ok := m.Sites[id]
	if !ok || site.ProjectID != projectID {
		return nil, nil
	}
	return &site, nil
}

func (m *MemoryStore) List(_ context.Context, projectID string) ([]*sites.Site, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*sites.Site{}
	for _, site := range m.Sites {
		if site.ProjectID == projectID {
			copied := site
			out = append(out, &copied)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *MemoryStore) UpdateSettings(_ context.Context, projectID, id string, spaFallback bool) (*sites.Site, error) {
	return m.edit(projectID, id, func(site *sites.Site) { site.SPAFallback = spaFallback })
}

func (m *MemoryStore) edit(projectID, id string, change func(*sites.Site)) (*sites.Site, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	site, ok := m.Sites[id]
	if !ok || site.ProjectID != projectID {
		return nil, sites.ErrSiteNotFound
	}
	change(&site)
	m.Sites[id] = site
	return &site, nil
}

func (m *MemoryStore) Delete(_ context.Context, projectID, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	site, ok := m.Sites[id]
	if !ok || site.ProjectID != projectID {
		return sites.ErrSiteNotFound
	}
	delete(m.Sites, id)
	for deployID, deploy := range m.Deploys {
		if deploy.SiteID == id {
			delete(m.Deploys, deployID)
		}
	}
	return nil
}

func (m *MemoryStore) ClaimDeploy(_ context.Context, projectID, id string, until time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	site, ok := m.Sites[id]
	if !ok || site.ProjectID != projectID {
		return sites.ErrSiteNotFound
	}
	if held, ok := m.Leases[id]; ok && held.After(m.Now()) {
		return sites.ErrSiteBusy
	}
	m.Leases[id] = until
	return nil
}

func (m *MemoryStore) ReleaseDeploy(_ context.Context, _, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.Leases, id)
	return nil
}

func (m *MemoryStore) MarkCorsClaimed(_ context.Context, projectID, id string) error {
	_, err := m.edit(projectID, id, func(site *sites.Site) { site.CorsClaimed = true })
	return err
}

func (m *MemoryStore) CreateDeploy(_ context.Context, deploy *sites.Deploy) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Deploys[deploy.ID] = *deploy
	return nil
}

func (m *MemoryStore) FinishDeploy(_ context.Context, deploy *sites.Deploy) error {
	m.mu.Lock()
	m.Deploys[deploy.ID] = *deploy
	m.mu.Unlock()
	if deploy.Status != sites.DeploySucceeded {
		return nil
	}
	_, err := m.edit(deploy.ProjectID, deploy.SiteID, func(site *sites.Site) { site.LiveDeployID = deploy.ID })
	return err
}

func (m *MemoryStore) SetLive(_ context.Context, projectID, siteID, deployID string) error {
	_, err := m.edit(projectID, siteID, func(site *sites.Site) { site.LiveDeployID = deployID })
	return err
}

func (m *MemoryStore) GetDeploy(_ context.Context, projectID, siteID, id string) (*sites.Deploy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	deploy, ok := m.Deploys[id]
	if !ok || deploy.ProjectID != projectID || deploy.SiteID != siteID {
		return nil, nil
	}
	return &deploy, nil
}

func (m *MemoryStore) ListDeploys(_ context.Context, projectID, siteID string, limit int) ([]*sites.Deploy, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*sites.Deploy{}
	for _, deploy := range m.Deploys {
		if deploy.ProjectID == projectID && deploy.SiteID == siteID {
			copied := deploy
			out = append(out, &copied)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Publisher stands in for Cloudflare; it asks for every hash it has not stored yet.
type Publisher struct {
	mu        sync.Mutex
	Stored    map[string]string
	Published map[string]cfworkers.AssetConfig
	Deleted   []string
	Sessions  int
	FailPut   error
	Forget    bool
}

func NewPublisher() *Publisher {
	return &Publisher{Stored: map[string]string{}, Published: map[string]cfworkers.AssetConfig{}}
}

func (f *Publisher) StartUploadSession(_ context.Context, _ string, manifest map[string]cfworkers.ManifestEntry) (cfworkers.UploadSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Sessions++
	if f.Forget {
		f.Stored = map[string]string{}
	}
	var missing []string
	for _, entry := range manifest {
		if _, ok := f.Stored[entry.Hash]; !ok {
			missing = append(missing, entry.Hash)
		}
	}
	if len(missing) == 0 {
		return cfworkers.UploadSession{JWT: "completion-from-session"}, nil
	}
	sort.Strings(missing)
	buckets := [][]string{}
	for _, hash := range missing {
		buckets = append(buckets, []string{hash})
	}
	return cfworkers.UploadSession{JWT: "upload-jwt", Buckets: buckets}, nil
}

func (f *Publisher) UploadBucket(_ context.Context, uploadJWT string, assets []cfworkers.Asset) (string, error) {
	if uploadJWT != "upload-jwt" {
		return "", errors.New("wrong upload token")
	}
	for _, asset := range assets {
		reader, err := asset.Open()
		if err != nil {
			return "", err
		}
		body := make([]byte, 1<<16)
		read, _ := reader.Read(body)
		_ = reader.Close()
		f.mu.Lock()
		f.Stored[asset.Hash] = string(body[:read])
		f.mu.Unlock()
	}
	return "completion-from-upload", nil
}

func (f *Publisher) PutScript(_ context.Context, script, completionJWT string, config cfworkers.AssetConfig) error {
	if f.FailPut != nil {
		return f.FailPut
	}
	if completionJWT != "completion-from-upload" && completionJWT != "completion-from-session" {
		return errors.New("published without a completion token")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Published[script] = config
	return nil
}

func (f *Publisher) DeleteScript(_ context.Context, script string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Deleted = append(f.Deleted, script)
	delete(f.Published, script)
	return nil
}

// Origins is the project CORS list with per-owner claims.
type Origins struct {
	mu      sync.Mutex
	Origins []string
	Owners  map[string][]string
	FailAdd error
}

func NewOrigins(existing ...string) *Origins {
	return &Origins{Origins: existing, Owners: map[string][]string{}}
}

func (f *Origins) AddCorsOrigin(_ context.Context, _, origin, owner string) (bool, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.FailAdd != nil {
		return false, nil, f.FailAdd
	}
	for _, existing := range f.Origins {
		if existing == origin {
			return false, f.Origins, nil
		}
	}
	f.Origins = append(f.Origins, origin)
	f.Owners[owner] = append(f.Owners[owner], origin)
	return true, f.Origins, nil
}

func (f *Origins) ReleaseAppCorsOrigins(_ context.Context, _, owner string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	released := f.Owners[owner]
	delete(f.Owners, owner)
	kept := []string{}
	for _, origin := range f.Origins {
		owned := false
		for _, mine := range released {
			owned = owned || mine == origin
		}
		if !owned {
			kept = append(kept, origin)
		}
	}
	f.Origins = kept
	return released, nil
}
