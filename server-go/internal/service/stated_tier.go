package service

import (
	"context"
	"strings"
)

// tierNotOrgPlanError is ErrTierNotOrgPlan for a create or add-database body
// that stated a tier, worded to point at the organization's plan (EXC-555).
type tierNotOrgPlanError struct{ orgPlan string }

func (e *tierNotOrgPlanError) Error() string {
	return "a project's plan comes from its organization (" + e.orgPlan +
		"); change the organization's plan instead"
}

func (e *tierNotOrgPlanError) Is(target error) bool { return target == ErrTierNotOrgPlan }

// ConfirmStatedTier accepts a tier a request states only when it is, in any
// case, the plan of orgID: the organization sizes the project, never the body.
func (s *ProvisioningService) ConfirmStatedTier(ctx context.Context, orgID, stated string) error {
	orgPlan, err := s.orgTier(ctx, orgID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(stated, string(orgPlan)) {
		return &tierNotOrgPlanError{orgPlan: string(orgPlan)}
	}
	return nil
}
