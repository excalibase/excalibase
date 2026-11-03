package service

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
)

// MajorUpgradeStatus is what Studio shows before and during an upgrade.
type MajorUpgradeStatus struct {
	Engine         string `json:"engine"`
	DeploymentMode string `json:"deploymentMode"`
	CurrentVersion string `json:"currentVersion"`
	// Targets are the majors offered from CurrentVersion.
	Targets   []string `json:"targets"`
	Supported bool     `json:"supported"`
	// Reason says why there is nothing to offer, in fixed wording.
	Reason string `json:"reason,omitempty"`
	// Blockers must be fixed before an upgrade can start.
	Blockers []string             `json:"blockers"`
	Downtime string               `json:"downtime,omitempty"`
	Latest   *domain.MajorUpgrade `json:"latest"`
}

const (
	downtimeKubernetes = "Every copy of the database stops while its data is converted, usually a few minutes whatever its size. " +
		"With more than one copy, writes then wait until one replica has copied the data again, which takes longer for a larger database."
	downtimeSingleHost = "The database stops taking connections while its data is copied into the new version, which takes longer for a larger database."
)

// Status reports the project's current version, the majors it may move to,
// what blocks it and its latest run. The checks run only against an active
// project with something to move to and no run in progress.
func (s *MajorUpgradeService) Status(ctx context.Context, projectID string) (*MajorUpgradeStatus, error) {
	inst, err := s.cfg.Instances.FindByProjectID(projectID)
	if err != nil || inst == nil {
		return nil, fmt.Errorf("project %s: %w", projectID, ErrProjectNotFound)
	}
	latest, err := s.cfg.Runs.LatestMajorUpgrade(ctx, projectID)
	if err != nil {
		return nil, err
	}
	status := &MajorUpgradeStatus{
		Engine: engineName(inst), DeploymentMode: string(modeOf(inst)), CurrentVersion: currentVersion(inst),
		Targets: []string{}, Blockers: []string{}, Latest: latest,
	}
	engine, reason := s.engineFor(inst)
	if engine == nil {
		status.Reason = reason
		return status, nil
	}
	status.Supported = true
	status.Downtime = downtimeFor(modeOf(inst))
	status.Targets = config.PostgresUpgradeTargets(inst.PostgresVersion)
	if len(status.Targets) == 0 {
		status.Reason = fmt.Sprintf("Postgres %s is the newest version offered here", inst.PostgresVersion)
		return status, nil
	}
	status.Blockers = s.blockersNow(ctx, engine, inst, latest, status.Targets[len(status.Targets)-1])
	return status, nil
}

func (s *MajorUpgradeService) blockersNow(ctx context.Context, engine MajorUpgrader, inst *domain.DatabaseInstance, latest *domain.MajorUpgrade, target string) []string {
	if latest != nil && !latest.Finished() {
		return []string{"an upgrade is running"}
	}
	if inst.Status != string(domain.StatusActive) {
		return []string{fmt.Sprintf("the project is %s; an upgrade needs a running database", inst.Status)}
	}
	if !s.backupsReady(inst) {
		return []string{ErrMajorUpgradeNeedsBackups.Error()}
	}
	if remembered, ok := s.checks.get(inst.ProjectID, target, s.cfg.Now()); ok {
		return remembered
	}
	checkCtx, cancel := context.WithTimeout(ctx, statusCheckTimeout)
	defer cancel()
	blockers, err := engine.Preflight(checkCtx, inst, target)
	if err != nil {
		log.Printf("major upgrade %s: preflight: %v", inst.ProjectID, err)
		return []string{ErrMajorUpgradeCheckUnavailable.Error()}
	}
	if blockers == nil {
		blockers = []string{}
	}
	s.checks.put(inst.ProjectID, target, blockers, s.cfg.Now())
	return blockers
}

// statusCheckTimeout bounds the database checks a status read runs.
const statusCheckTimeout = 2 * time.Minute

// statusCheckTTL is how long a status read reuses a project's checks.
const statusCheckTTL = time.Minute

// recentChecks remembers each project's last checks for statusCheckTTL, so
// reading the status, open to any member, cannot keep the database busy.
type recentChecks struct {
	mu      sync.Mutex
	entries map[string]rememberedCheck
}

type rememberedCheck struct {
	target   string
	at       time.Time
	blockers []string
}

func (c *recentChecks) get(projectID, target string, now time.Time) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[projectID]
	if !ok || entry.target != target || now.Sub(entry.at) >= statusCheckTTL {
		return nil, false
	}
	return append([]string{}, entry.blockers...), true
}

func (c *recentChecks) put(projectID, target string, blockers []string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]rememberedCheck{}
	}
	for id, entry := range c.entries {
		if now.Sub(entry.at) >= statusCheckTTL {
			delete(c.entries, id)
		}
	}
	c.entries[projectID] = rememberedCheck{target: target, at: now, blockers: append([]string{}, blockers...)}
}

func engineName(inst *domain.DatabaseInstance) string {
	if inst.DBType == "" {
		return string(domain.PostgreSQL)
	}
	return string(inst.DBType)
}

func currentVersion(inst *domain.DatabaseInstance) string {
	if inst.DBType == domain.MySQL {
		return inst.MySQLVersion
	}
	return inst.PostgresVersion
}

func modeOf(inst *domain.DatabaseInstance) domain.DeploymentMode {
	if inst.DeploymentMode == "" {
		return domain.ModeK8s
	}
	return inst.DeploymentMode
}

func downtimeFor(mode domain.DeploymentMode) string {
	if mode == domain.ModeDocker {
		return downtimeSingleHost
	}
	return downtimeKubernetes
}
