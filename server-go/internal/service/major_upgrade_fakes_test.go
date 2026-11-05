package service

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/storage"
)

// memMajorUpgrades is MajorUpgradeStore in memory, with the real store's
// guards: writes need the run RUNNING and owned by the writer, and a project
// status goes in with its run's write.
type memMajorUpgrades struct {
	mu        sync.Mutex
	runs      map[string]*domain.MajorUpgrade
	instances storage.InstanceStore
	markErr   error
	// markLandsErr commits the mark, then answers this error.
	markLandsErr error
	// waitingErr fails writes of a run's waiting step.
	waitingErr error
	// latestErrs fail the next reads of the latest run, in order.
	latestErrs []error
	// beforeClaim runs once, just before a claim.
	beforeClaim func()
	// touchErrs fail the next heartbeats, in order; touchErr fails the rest.
	touchErrs []error
	touchErr  error
	// touchHang makes a heartbeat wait until its context ends.
	touchHang bool
	touches   int
	// clock is the database's clock when set.
	clock func() time.Time
}

// now is the database's clock; heartbeats are stamped with it.
func (m *memMajorUpgrades) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

func (m *memMajorUpgrades) setTouchErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.touchErr = err
}

func (m *memMajorUpgrades) failTouches(n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := 0; i < n; i++ {
		m.touchErrs = append(m.touchErrs, err)
	}
}

func (m *memMajorUpgrades) setTouchHang(hang bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.touchHang = hang
}

func (m *memMajorUpgrades) touchCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.touches
}

func newMemMajorUpgrades(instances storage.InstanceStore) *memMajorUpgrades {
	return &memMajorUpgrades{runs: map[string]*domain.MajorUpgrade{}, instances: instances}
}

func (m *memMajorUpgrades) StartMajorUpgrade(_ context.Context, run *domain.MajorUpgrade) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.runs {
		if existing.ProjectID == run.ProjectID && existing.Status == domain.MajorUpgradeRunning {
			return storage.ErrMajorUpgradeRunning
		}
	}
	if run.StartedAt.IsZero() {
		run.StartedAt = time.Now().UTC()
	}
	run.Status = domain.MajorUpgradeRunning
	run.HeartbeatAt = run.StartedAt
	stored := *run
	m.runs[run.ID] = &stored
	return nil
}

// owned is the stored run when owner still holds it RUNNING.
func (m *memMajorUpgrades) owned(id, owner string) (*domain.MajorUpgrade, bool) {
	stored, ok := m.runs[id]
	return stored, ok && stored.Status == domain.MajorUpgradeRunning && stored.Owner == owner
}

func (m *memMajorUpgrades) UpdateMajorUpgrade(_ context.Context, run *domain.MajorUpgrade, project *storage.MajorUpgradeProject) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.owned(run.ID, run.Owner)
	if !ok || (stored.Converted && run.Status != domain.MajorUpgradeRunning && run.Status != domain.MajorUpgradeCompleted) {
		return storage.ErrMajorUpgradeNotRunning
	}
	if m.waitingErr != nil && run.Step == domain.MajorUpgradeStepWaiting {
		return m.waitingErr
	}
	if project != nil {
		row, err := m.instances.FindByProjectID(run.ProjectID)
		if err != nil {
			return err
		}
		row.Status, row.CurrentStep, row.FailureReason = project.Status, project.Step, project.Reason
		if err := m.instances.Update(row); err != nil {
			return err
		}
	}
	// The columns the real store writes, and only those.
	stored.Status, stored.Step, stored.SafetyBackupID, stored.Error = run.Status, run.Step, run.SafetyBackupID, run.Error
	stored.Warnings, stored.FinishedAt, stored.WritesStoppedAt = run.Warnings, run.FinishedAt, run.WritesStoppedAt
	return nil
}

func (m *memMajorUpgrades) TouchMajorUpgrade(ctx context.Context, id, owner string) error {
	m.mu.Lock()
	m.touches++
	hang := m.touchHang
	m.mu.Unlock()
	if hang {
		<-ctx.Done()
		return ctx.Err()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.touchErrs) > 0 {
		err := m.touchErrs[0]
		m.touchErrs = m.touchErrs[1:]
		return err
	}
	if m.touchErr != nil {
		return m.touchErr
	}
	stored, ok := m.owned(id, owner)
	if !ok {
		return storage.ErrMajorUpgradeNotRunning
	}
	stored.HeartbeatAt = m.now()
	return nil
}

func (m *memMajorUpgrades) MarkMajorUpgradeConverted(_ context.Context, id, projectID, toVersion, owner string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.markErr != nil {
		return m.markErr
	}
	stored, ok := m.owned(id, owner)
	if !ok || stored.ProjectID != projectID || stored.Converted {
		return storage.ErrMajorUpgradeNotRunning
	}
	inst, err := m.instances.FindByProjectID(projectID)
	if err != nil {
		return err
	}
	inst.PostgresVersion = toVersion
	if err := m.instances.Update(inst); err != nil {
		return err
	}
	stored.Converted = true
	stored.ConvertedAt = &at
	stored.HeartbeatAt = m.now()
	return m.markLandsErr
}

func (m *memMajorUpgrades) ClaimMajorUpgrade(_ context.Context, id, owner string, staleAfter time.Duration) (*domain.MajorUpgrade, error) {
	if hook := m.beforeClaim; hook != nil {
		m.beforeClaim = nil
		hook()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stored, ok := m.runs[id]
	if !ok || stored.Status != domain.MajorUpgradeRunning || !stored.HeartbeatAt.Before(m.now().Add(-staleAfter)) {
		return nil, nil
	}
	stored.Owner, stored.HeartbeatAt = owner, m.now()
	claimed := *stored
	return &claimed, nil
}

func (m *memMajorUpgrades) LatestMajorUpgrade(_ context.Context, projectID string) (*domain.MajorUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.latestErrs) > 0 {
		err := m.latestErrs[0]
		m.latestErrs = m.latestErrs[1:]
		return nil, err
	}
	var latest *domain.MajorUpgrade
	for _, run := range m.runs {
		if run.ProjectID == projectID && (latest == nil || run.StartedAt.After(latest.StartedAt)) {
			copied := *run
			latest = &copied
		}
	}
	return latest, nil
}

func (m *memMajorUpgrades) ConvertedMajorUpgrades(_ context.Context, projectID string) ([]domain.MajorUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.MajorUpgrade{}
	for _, run := range m.runs {
		if run.ProjectID == projectID && run.Converted {
			out = append(out, *run)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConvertedAt.Before(*out[j].ConvertedAt) })
	return out, nil
}

func (m *memMajorUpgrades) RunningMajorUpgrades(context.Context) ([]domain.MajorUpgrade, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []domain.MajorUpgrade{}
	for _, run := range m.runs {
		if run.Status == domain.MajorUpgradeRunning {
			out = append(out, *run)
		}
	}
	return out, nil
}

// setRun changes the stored run in place, as another actor would.
func (m *memMajorUpgrades) setRun(id string, change func(run *domain.MajorUpgrade)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	change(m.runs[id])
}
