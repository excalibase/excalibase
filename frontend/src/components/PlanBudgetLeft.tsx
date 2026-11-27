import { cpuText, memoryText, useOrgPlanBudget } from '../api/planBudget';

// What is left of the org's plan, beside a form that would spend it. Silent while the budget is off or unread.
export function PlanBudgetLeft({ orgId }: { readonly orgId: string | undefined }) {
  const { data } = useOrgPlanBudget(orgId);
  if (!data) return null;
  const { left } = data;
  return (
    <p className="text-xs text-text-secondary" data-testid="plan-budget-left">
      Left in your plan: {cpuText(Math.max(left.cpuMilli, 0))}, {memoryText(Math.max(left.memoryBytes, 0))} memory, {memoryText(Math.max(left.diskBytes, 0))} disk
    </p>
  );
}
