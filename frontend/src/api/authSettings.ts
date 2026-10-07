import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

export interface AuthSettings {
  requireEmailVerification: boolean;
  siteUrl: string;
}

const authSettingsKey = (projectId: string) => ['auth-settings', projectId] as const;
const authSettingsPath = (projectId: string) => `/projects/${projectId}/auth-settings`;

export const SITE_URL_RULE =
  'Use an absolute https URL such as https://app.example.com (http is accepted only for localhost), with no query, fragment or trailing slash.';

const LOOPBACK_HOSTS = ['localhost', '127.0.0.1', '[::1]'];

// Mirrors the server's rule so a mistake is caught before the request. An
// empty value is allowed here: it means "not set yet".
export function siteUrlProblem(raw: string): string | null {
  const value = raw.trim();
  if (value === '') return null;
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    return `That is not an absolute URL. ${SITE_URL_RULE}`;
  }
  const loopback = LOOPBACK_HOSTS.includes(parsed.hostname);
  if (parsed.protocol !== 'https:' && !(parsed.protocol === 'http:' && loopback)) {
    return `${parsed.protocol === 'http:' ? 'Plain http is only allowed for localhost.' : 'The URL must start with https://.'} ${SITE_URL_RULE}`;
  }
  if (parsed.username || parsed.password) return `Remove the user and password from the URL. ${SITE_URL_RULE}`;
  if (parsed.search || parsed.hash || value.includes('?') || value.includes('#')) {
    return `Remove the query and fragment from the URL. ${SITE_URL_RULE}`;
  }
  if (value.endsWith('/')) return `Remove the trailing slash. ${SITE_URL_RULE}`;
  return null;
}

// The first app that already has a public address, offered as the site URL.
export function suggestSiteUrl(apps: ReadonlyArray<{ url?: string }> | undefined): string | null {
  for (const app of apps ?? []) {
    const candidate = (app.url ?? '').trim().replace(/\/+$/, '');
    if (candidate !== '' && siteUrlProblem(candidate) === null) return candidate;
  }
  return null;
}

export const useAuthSettings = (projectId: string) =>
  useQuery({
    queryKey: authSettingsKey(projectId),
    queryFn: async () => (await api.get<AuthSettings>(authSettingsPath(projectId))).data,
    enabled: !!projectId,
  });

export const useSaveAuthSettings = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (settings: AuthSettings) =>
      (await api.put<AuthSettings>(authSettingsPath(projectId), settings)).data,
    onSuccess: (saved) => queryClient.setQueryData(authSettingsKey(projectId), saved),
  });
};
