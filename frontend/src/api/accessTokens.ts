import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// The caller's personal access tokens, as GET /api/auth/tokens lists them.
// Sign-in sessions and service tokens are never in this list. `id` is the
// SHA-256 of the secret, which revoke takes; the secret itself exists only in
// the response that created it.
export interface AccessToken {
  id: string;
  tokenPrefix: string;
  name: string;
  // Comma-separated; empty is a legacy all-purpose token.
  scopes: string;
  projectId?: string;
  createdAt?: string;
  expiresAt?: string | null;
  lastUsed?: string;
}

export interface CreatedAccessToken {
  token: string;
  prefix: string;
  name: string;
  scopes: string;
  projectId?: string;
  expiresAt?: string | null;
}

// What the page lets a person ask for. A token acts with its owner's own
// role on every request, so neither choice can exceed the owner's rights.
export type TokenAccess = 'read' | 'write';

export interface CreateAccessTokenInput {
  name: string;
  access: TokenAccess;
  expiresIn: string;
  projectId: string;
}

const TOKENS_PATH = '/auth/tokens';
const TOKENS_KEY = ['access-tokens'];

export function scopeLabel(scopes: string): string {
  const set = new Set(scopes.split(',').map((s) => s.trim()).filter(Boolean));
  if (set.size === 0) return 'Full access (legacy)';
  if (set.has('write') || set.has('admin')) return 'Read and write';
  return 'Read only';
}

export function useAccessTokens() {
  return useQuery({
    queryKey: TOKENS_KEY,
    queryFn: async () => (await api.get<AccessToken[]>(TOKENS_PATH)).data,
  });
}

export function useCreateAccessToken() {
  const client = useQueryClient();
  return useMutation({
    // The response carries the secret: drop it from the mutation cache at once.
    gcTime: 0,
    mutationFn: async (input: CreateAccessTokenInput) => {
      const body: Record<string, unknown> = {
        name: input.name,
        scopes: input.access === 'write' ? ['read', 'write'] : ['read'],
        expiresIn: input.expiresIn,
      };
      if (input.projectId) body.projectId = input.projectId;
      return (await api.post<CreatedAccessToken>(TOKENS_PATH, body)).data;
    },
    onSuccess: () => client.invalidateQueries({ queryKey: TOKENS_KEY }),
  });
}

export function useRevokeAccessToken() {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      await api.delete(`${TOKENS_PATH}/${encodeURIComponent(id)}`);
    },
    onSuccess: () => client.invalidateQueries({ queryKey: TOKENS_KEY }),
  });
}
