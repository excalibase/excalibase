package service

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/config"
	"github.com/excalibase/provisioning-poc/internal/domain"
	"github.com/excalibase/provisioning-poc/internal/features"
)

// NamespacePlans reads and writes the plan label the log collector copies
// onto a project's lines, which Loki keeps for that plan (EXC-659).
type NamespacePlans interface {
	ProjectNamespacePlans(ctx context.Context) (map[string]string, error)
	SetNamespacePlan(ctx context.Context, namespace, plan string) error
}

// logRetention is how long each plan's project logs are kept, while its flag is on.
type logRetention struct {
	flags   features.Flags
	periods config.LogRetention
	plans   NamespacePlans
}

// SetLogRetention keeps each project's logs for its plan: its namespace is
// labelled with the plan and its searches reach no further back than kept.
func (s *ProvisioningService) SetLogRetention(flags features.Flags, periods config.LogRetention, plans NamespacePlans) {
	s.logRetention = &logRetention{flags: flags, periods: periods, plans: plans}
}

func (s *ProvisioningService) logRetentionOn(ctx context.Context) bool {
	return s.logRetention != nil && features.Enabled(ctx, s.logRetention.flags, features.LogRetention)
}

// logsKeptFor is how long the project's logs are kept; zero with the flag off.
func (s *ProvisioningService) logsKeptFor(ctx context.Context, inst *domain.DatabaseInstance) time.Duration {
	if !s.logRetentionOn(ctx) {
		return 0
	}
	return s.logRetention.periods.For(inst.Tier)
}

// logPlanLabel is the namespace label value for tier; empty for none or an unknown tier.
func logPlanLabel(inst *domain.DatabaseInstance) string {
	if inst.Namespace == "" || inst.DeploymentMode == domain.ModeDocker || !domain.IsValidTier(inst.Tier) {
		return ""
	}
	return strings.ToLower(string(inst.Tier))
}

// relabelLogPlan labels the project's namespace with its recorded plan. A
// failure is logged, not returned: the sweep writes the label later.
func (s *ProvisioningService) relabelLogPlan(ctx context.Context, projectID string) {
	if !s.logRetentionOn(ctx) {
		return
	}
	inst, err := s.GetInstance(projectID)
	if err != nil {
		log.Printf("log plan of %s: %v", projectID, err)
		return
	}
	if plan := logPlanLabel(inst); plan != "" {
		if err := s.logRetention.plans.SetNamespacePlan(ctx, inst.Namespace, plan); err != nil {
			log.Printf("log plan of %s: %v", projectID, err)
		}
	}
}

// SyncLogPlans labels every project namespace whose plan label differs from
// its project's plan, and answers how many it wrote. Lines already stored keep
// the plan they were stored under; only new lines take the new one.
func (s *ProvisioningService) SyncLogPlans(ctx context.Context) (int, error) {
	if !s.logRetentionOn(ctx) {
		return 0, nil
	}
	current, err := s.logRetention.plans.ProjectNamespacePlans(ctx)
	if err != nil {
		return 0, err
	}
	instances, err := s.store.FindAll()
	if err != nil {
		return 0, err
	}
	written := 0
	var failures []error
	for _, inst := range instances {
		plan := logPlanLabel(inst)
		label, exists := current[inst.Namespace]
		if plan == "" || !exists || label == plan {
			continue
		}
		if err := s.logRetention.plans.SetNamespacePlan(ctx, inst.Namespace, plan); err != nil {
			failures = append(failures, err)
			continue
		}
		written++
	}
	return written, errors.Join(failures...)
}

// RunLogPlanSweep labels project namespaces now and every interval until ctx
// ends, so a new or restored project's logs take its plan within one interval.
func (s *ProvisioningService) RunLogPlanSweep(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if written, err := s.SyncLogPlans(ctx); err != nil {
			log.Printf("log plan sweep: %v", err)
		} else if written > 0 {
			log.Printf("log plan sweep: labelled %d project namespaces", written)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
