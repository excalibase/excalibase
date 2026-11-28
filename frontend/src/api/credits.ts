import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';
import { API_BASE } from './base';

// What the credit routes answer (EXC-866). Money is whole micro-dollars; every
// amount a person reads goes through dollars() so no float arithmetic touches it.

export interface CreditLot {
  id: number;
  kind: string;
  originalMicros: number;
  remainingMicros: number;
  expiresAt: string | null;
  createdAt: string;
}

export interface CreditEntry {
  id: number;
  kind: 'grant_trial' | 'grant_admin' | 'adjust_admin' | 'usage' | 'expire';
  amountMicros: number;
  actor: string;
  reason: string;
  periodStart?: string;
  periodEnd?: string;
  createdAt: string;
}

export type TrialState = 'none' | 'needs_terms' | 'pending' | 'waitlisted' | 'granted' | 'refused';

export interface CreditBurn {
  perDayMicros: number;
  days: { day: string; micros: number }[];
  runOutAt: string | null;
}

export interface CreditOverview {
  orgId: string;
  balanceMicros: number;
  lots: CreditLot[];
  entries: CreditEntry[];
  burn: CreditBurn;
  metering: boolean;
  nextExpiry: string | null;
  trial: { state: TrialState; position?: number; amountMicros: number; days: number };
  billedIn: string;
  termsVersion: string;
  termsUrl: string;
}

export const creditsKey = (orgId: string | undefined) => ['credits', orgId] as const;

export function useOrgCredits(orgId: string | undefined, enabled: boolean) {
  return useQuery({
    queryKey: creditsKey(orgId),
    queryFn: async () => (await api.get<CreditOverview>(`/orgs/${orgId}/credits/`)).data,
    enabled: enabled && Boolean(orgId),
    staleTime: 30_000,
  });
}

export function useAcceptCreditTerms(orgId: string | undefined) {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (version: string) => (await api.post<{ accepted: boolean; outcome: string }>('/credits/terms', { version })).data,
    onSuccess: () => client.invalidateQueries({ queryKey: creditsKey(orgId) }),
  });
}

export interface AdminCreditChange {
  orgId: string;
  amountMicros: number;
  reason: string;
  expiresAt?: string;
  key: string;
}

// An admin's grant (positive) or take-back (negative); the key makes a retried click one entry.
export function useAdminCreditChange() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async ({ orgId, key, ...body }: AdminCreditChange) =>
      (await api.post(`/admin/console/orgs/${orgId}/credits`, body, { headers: { 'Idempotency-Key': key } })).data,
    onSuccess: (_data, { orgId }) => client.invalidateQueries({ queryKey: creditsKey(orgId) }),
  });
}

// The session cookie is the credential, so a plain link downloads the CSV.
export function statementCsvHref(orgId: string, month: string): string {
  return `${API_BASE}/orgs/${orgId}/credits/statement?month=${encodeURIComponent(month)}&format=csv`;
}
