import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

export interface AuthSettings {
  requireEmailVerification: boolean;
  siteUrl: string;
  // Only while the authoauth or authotp feature is on (EXC-662); a PUT without them keeps the stored ones.
  redirectUrls?: string[];
  allowNewUsers?: boolean;
  // Only while the authotp feature is on.
  emailOtpEnabled?: boolean;
  // Only while the authguest feature is on; the Turnstile secret itself is write-only.
  guestSignInEnabled?: boolean;
  guestCaptchaSecretSet?: boolean;
}

const authSettingsKey = (projectId: string) => ['auth-settings', projectId] as const;
const authSettingsPath = (projectId: string) => `/projects/${projectId}/auth-settings`;

export const SITE_URL_RULE =
  'Use an absolute https URL such as https://app.example.com (http is accepted only for localhost), with no query, fragment or trailing slash.';

const LOOPBACK_HOSTS = new Set(['localhost', '127.0.0.1', '[::1]']);

function withoutTrailingSlashes(value: string): string {
  let end = value.length;
  while (end > 0 && value[end - 1] === '/') end -= 1;
  return value.slice(0, end);
}

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
  const loopback = LOOPBACK_HOSTS.has(parsed.hostname);
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

// A redirect URL follows the site URL's rule, and an empty one is not a URL.
export function redirectUrlProblem(raw: string): string | null {
  if (raw.trim() === '') return 'Enter the URL your app returns to after sign-in.';
  return siteUrlProblem(raw);
}

// The first app that already has a public address, offered as the site URL.
export function suggestSiteUrl(apps: ReadonlyArray<{ url?: string }> | undefined): string | null {
  for (const app of apps ?? []) {
    const candidate = withoutTrailingSlashes((app.url ?? '').trim());
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

// The guest Turnstile secret is written and removed on its own path; the
// answer carries the sign-in fields, merged into the cached settings.
export const useGuestCaptcha = (projectId: string) => {
  const queryClient = useQueryClient();
  const merge = (answer: Partial<AuthSettings>) =>
    queryClient.setQueryData<AuthSettings>(authSettingsKey(projectId), (current) => (current ? { ...current, ...answer } : current));
  return {
    save: useMutation({
      mutationFn: async (secretKey: string) =>
        (await api.put<Partial<AuthSettings>>(`${authSettingsPath(projectId)}/guest-captcha`, { secretKey })).data,
      onSuccess: merge,
    }),
    remove: useMutation({
      mutationFn: async () => (await api.delete<Partial<AuthSettings>>(`${authSettingsPath(projectId)}/guest-captcha`)).data,
      onSuccess: merge,
    }),
  };
};

export const useSaveAuthSettings = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (settings: AuthSettings) =>
      (await api.put<AuthSettings>(authSettingsPath(projectId), settings)).data,
    onSuccess: (saved) => queryClient.setQueryData(authSettingsKey(projectId), saved),
  });
};
