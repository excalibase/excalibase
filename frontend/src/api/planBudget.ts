import { useQuery } from '@tanstack/react-query';
import { api } from './client';
import { useFeatureEnabled } from '../hooks/useDeploymentMode';

// What the plan-budget route answers (EXC-901). Sizes are whole units: milli-vCPU and bytes.
export interface BudgetSize {
  cpuMilli: number;
  memoryBytes: number;
  diskBytes: number;
  services: number;
}

export interface BudgetService {
  kind: 'database' | 'app' | 'functions';
  name: string;
  projectId: string;
  size: BudgetSize;
  unrecorded?: boolean;
}

export interface PlanBudget {
  enabled: boolean;
  plan: string;
  budget: BudgetSize;
  used: BudgetSize;
  left: BudgetSize;
  services: BudgetService[];
}

export const planBudgetKey = (orgId: string | undefined) => ['plan-budget', orgId] as const;

// The org's budget, read only while the feature is on. `data` is undefined while off, loading or failed.
export function useOrgPlanBudget(orgId: string | undefined) {
  const feature = useFeatureEnabled('planbudget');
  const query = useQuery({
    queryKey: planBudgetKey(orgId),
    queryFn: async () => (await api.get<PlanBudget>(`/orgs/${orgId}/plan-budget`)).data,
    enabled: feature.enabled && Boolean(orgId),
    staleTime: 15_000,
  });
  const data = query.data?.enabled ? query.data : undefined;
  return { data, isLoading: feature.enabled && query.isLoading, isError: feature.enabled && query.isError };
}

const GIB = 1024 ** 3;
const MIB = 1024 ** 2;

const trimmed = (value: number) => String(Number(value.toFixed(2)));

export const cpuText = (milli: number) => `${trimmed(milli / 1000)} vCPU`;

export function memoryText(bytes: number): string {
  return bytes >= GIB ? `${trimmed(bytes / GIB)} GiB` : `${trimmed(bytes / MIB)} MiB`;
}

// A refusal the plan budget made: the server's own sentence, and the dimension that did not fit.
export interface BudgetRefusal {
  message: string;
  dimension: string;
}

export function budgetRefusal(error: unknown): BudgetRefusal | undefined {
  const response = (error as { response?: { status?: number; data?: { code?: unknown; error?: unknown; dimension?: unknown } } } | null | undefined)?.response;
  if (response?.status !== 409 || response.data?.code !== 'plan_budget_exceeded') return undefined;
  return { message: String(response.data.error ?? ''), dimension: String(response.data.dimension ?? '') };
}
