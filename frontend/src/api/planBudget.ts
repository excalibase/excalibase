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
  // The ceiling is the free allowance because the credit is used up.
  creditExhausted: boolean;
  // The step alone is no bigger than the ceiling, so it would start once other services are paused.
  fitsCeilingAlone: boolean;
}

interface RefusalBody {
  code?: unknown;
  error?: unknown;
  dimension?: unknown;
  creditExhausted?: unknown;
  step?: unknown;
  budget?: unknown;
}

const textOf = (value: unknown): string => (typeof value === 'string' ? value : '');

const numberOf = (size: unknown, key: 'cpuMilli' | 'memoryBytes'): number => {
  const value = (size as Record<string, unknown> | null | undefined)?.[key];
  return typeof value === 'number' ? value : Number.NaN;
};

// The whole step against the ceiling, in CPU and memory. False when either size is missing: an unreadable answer never claims the step would fit.
function fitsAlone(step: unknown, budget: unknown): boolean {
  return numberOf(step, 'cpuMilli') <= numberOf(budget, 'cpuMilli') && numberOf(step, 'memoryBytes') <= numberOf(budget, 'memoryBytes');
}

export function budgetRefusal(error: unknown): BudgetRefusal | undefined {
  const response = (error as { response?: { status?: number; data?: RefusalBody } } | null | undefined)?.response;
  if (response?.status !== 409 || response.data?.code !== 'plan_budget_exceeded') return undefined;
  return { message: textOf(response.data.error), dimension: textOf(response.data.dimension), creditExhausted: response.data.creditExhausted === true, fitsCeilingAlone: fitsAlone(response.data.step, response.data.budget) };
}
