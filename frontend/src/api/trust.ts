import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// What GET /api/trust says about the signed-in account (EXC-899). Only counts and ages: the GitHub account's id stays on the server.
export interface TrustAttempt {
  checkedAt: string;
  passed: boolean;
  reason: string;
  missing: string[];
  evidence: { accountAgeDays: number; publicEvents: number; publicRepos: number; oldestRepoAgeDays: number };
}

export interface TrustStatus {
  state: 'limited' | 'full';
  emailVerified: boolean;
  githubConfigured: boolean;
  policy: { minAgeDays: number; minPublicEvents: number; activityWindowDays: number; minRepos: number; repoMinAgeDays: number };
  retryAfterSeconds?: number;
  lastAttempt?: TrustAttempt;
}

const trustKey = ['trust'] as const;

// Off until the server runs the limited trial, so Studio never calls a route that answers 404.
export const useTrust = (enabled: boolean) =>
  useQuery({ queryKey: trustKey, enabled, queryFn: async () => (await api.get<TrustStatus>('/trust/')).data, staleTime: 15_000 });

// Starts the GitHub round trip; the page then goes to the URL it answers.
export function useStartGitHubVerify() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async () => (await api.post<{ url: string }>('/trust/github/start')).data.url,
    onSuccess: (url) => {
      void client.invalidateQueries({ queryKey: trustKey });
      globalThis.location.assign(url);
    },
  });
}

// True when a request was refused because the account is not verified: the 403 carries this code.
export function isTrustRequired(err: unknown): boolean {
  const response = (err as { response?: { status?: number; data?: { code?: unknown } } } | null)?.response;
  return response?.status === 403 && response.data?.code === 'trust_required';
}

export function trustFailure(err: unknown): string {
  const response = (err as { response?: { data?: { error?: unknown } } } | null)?.response;
  const message = response?.data?.error;
  return typeof message === 'string' && message.trim() !== '' ? message : 'Verification could not be started. Try again later.';
}
