package sitestest

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/sites"
)

// MemoryPreviews is an in-memory PreviewStore with the Postgres store's refusals.
type MemoryPreviews struct {
	mu       sync.Mutex
	sites    *MemoryStore
	Previews map[string]sites.Preview
	leases   map[string]Lease
}

func NewMemoryPreviews(siteStore *MemoryStore) *MemoryPreviews {
	return &MemoryPreviews{sites: siteStore, Previews: map[string]sites.Preview{}, leases: map[string]Lease{}}
}

func (m *MemoryPreviews) now() time.Time { return m.sites.Now() }

func (m *MemoryPreviews) OpenPreview(_ context.Context, preview *sites.Preview, maxPerProject, maxInstall int) (*sites.Preview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inProject := 0
	for id, held := range m.Previews {
		if held.SiteID == preview.SiteID && held.Source == preview.Source && held.Ref == preview.Ref {
			if held.Status == sites.PreviewRemoving {
				return nil, sites.ErrPreviewRemoving
			}
			held.Status, held.CommitSHA, held.UpdatedAt = sites.PreviewBuilding, preview.CommitSHA, preview.UpdatedAt
			m.Previews[id] = held
			return &held, nil
		}
		if held.ProjectID == preview.ProjectID {
			inProject++
		}
	}
	if inProject >= maxPerProject {
		return nil, sites.ErrPreviewLimitReached
	}
	if len(m.Previews) >= maxInstall {
		return nil, sites.ErrPreviewInstallFull
	}
	m.Previews[preview.ID] = *preview
	created := *preview
	return &created, nil
}

func (m *MemoryPreviews) GetPreview(_ context.Context, projectID, siteID, id string) (*sites.Preview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.Previews[id]
	if !ok || held.ProjectID != projectID || held.SiteID != siteID {
		return nil, nil
	}
	return &held, nil
}

func (m *MemoryPreviews) FindPreview(_ context.Context, projectID, siteID, source, ref string) (*sites.Preview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, held := range m.Previews {
		if held.ProjectID == projectID && held.SiteID == siteID && held.Source == source && held.Ref == ref {
			found := held
			return &found, nil
		}
	}
	return nil, nil
}

func (m *MemoryPreviews) ListPreviews(_ context.Context, projectID, siteID string) ([]*sites.Preview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*sites.Preview{}
	for _, held := range m.Previews {
		if held.ProjectID == projectID && held.SiteID == siteID {
			copied := held
			out = append(out, &copied)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *MemoryPreviews) CountPreviews(_ context.Context, projectID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, held := range m.Previews {
		if held.ProjectID == projectID {
			count++
		}
	}
	return count, nil
}

func (m *MemoryPreviews) ClaimPreview(_ context.Context, projectID, id, lease string, until time.Time, removing bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.Previews[id]
	if !ok || held.ProjectID != projectID || (held.Status == sites.PreviewRemoving && !removing) {
		return sites.ErrPreviewNotFound
	}
	if current, ok := m.leases[id]; ok && current.Until.After(m.now()) {
		return sites.ErrSiteBusy
	}
	m.leases[id] = Lease{Token: lease, Until: until}
	return nil
}

func (m *MemoryPreviews) ReleasePreview(_ context.Context, _, id, lease string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.leases[id].Token == lease {
		delete(m.leases, id)
	}
	return nil
}

func (m *MemoryPreviews) holds(id, lease string) bool {
	current, ok := m.leases[id]
	return ok && current.Token == lease && current.Until.After(m.now())
}

func (m *MemoryPreviews) FinishPreview(_ context.Context, preview *sites.Preview, lease string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.Previews[preview.ID]
	if !ok {
		return sites.ErrPreviewNotFound
	}
	if !m.holds(preview.ID, lease) {
		return sites.ErrLeaseLost
	}
	updated := *preview
	if held.Status == sites.PreviewRemoving {
		updated.Status = sites.PreviewRemoving
	}
	m.Previews[preview.ID] = updated
	return nil
}

func (m *MemoryPreviews) MarkPreviewRemoving(_ context.Context, projectID, id string, idleBefore time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.Previews[id]
	if !ok || held.ProjectID != projectID {
		return sites.ErrPreviewNotFound
	}
	if !idleBefore.IsZero() && held.Status != sites.PreviewRemoving && !held.UpdatedAt.Before(idleBefore) {
		return sites.ErrPreviewNotFound
	}
	held.Status = sites.PreviewRemoving
	m.Previews[id] = held
	return nil
}

func (m *MemoryPreviews) DeletePreview(_ context.Context, projectID, id, lease string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	held, ok := m.Previews[id]
	if !ok || held.ProjectID != projectID {
		return sites.ErrPreviewNotFound
	}
	if !m.holds(id, lease) {
		return sites.ErrLeaseLost
	}
	delete(m.Previews, id)
	delete(m.leases, id)
	return nil
}

func (m *MemoryPreviews) DuePreviews(_ context.Context, idleBefore time.Time, limit int) ([]*sites.Preview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []*sites.Preview{}
	for _, held := range m.Previews {
		if held.Status == sites.PreviewRemoving || held.UpdatedAt.Before(idleBefore) {
			copied := held
			out = append(out, &copied)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		removingI, removingJ := out[i].Status == sites.PreviewRemoving, out[j].Status == sites.PreviewRemoving
		if removingI != removingJ {
			return removingI
		}
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
