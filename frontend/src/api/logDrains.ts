import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// Log drains (EXC-658): forward a project's logs to Datadog or an HTTPS endpoint.

export type LogDrainKind = 'datadog' | 'https';
export type LogDrainStatus = 'active' | 'failing' | 'disabled';

export interface LogDrain {
  id: string;
  name: string;
  kind: LogDrainKind;
  datadogSite?: string;
  url?: string;
  headerName?: string;
  // The secret itself is never returned.
  hasSecret: boolean;
  enabled: boolean;
  status: LogDrainStatus;
  lastError?: string;
  consecutiveFailures: number;
  lastSuccessAt?: string;
  sentLines: number;
  droppedLines: number;
  createdAt?: string;
}

export interface CreateLogDrain {
  name: string;
  kind: LogDrainKind;
  datadogSite?: string;
  url?: string;
  headerName?: string;
  secret?: string;
}

export interface UpdateLogDrain {
  name?: string;
  enabled?: boolean;
  datadogSite?: string;
  url?: string;
  headerName?: string;
  // Omitted keeps the stored secret.
  secret?: string;
}

export const MAX_LOG_DRAINS = 3;

export const DATADOG_SITES = [
  { value: 'us1', label: 'US1', host: 'datadoghq.com' },
  { value: 'us3', label: 'US3', host: 'us3.datadoghq.com' },
  { value: 'us5', label: 'US5', host: 'us5.datadoghq.com' },
  { value: 'eu1', label: 'EU1', host: 'datadoghq.eu' },
  { value: 'ap1', label: 'AP1', host: 'ap1.datadoghq.com' },
  { value: 'ap2', label: 'AP2', host: 'ap2.datadoghq.com' },
  { value: 'us1-fed', label: 'US1-FED', host: 'ddog-gov.com' },
] as const;

export function drainDestination(drain: LogDrain): string {
  if (drain.kind === 'https') return drain.url ?? '';
  const site = DATADOG_SITES.find((entry) => entry.value === drain.datadogSite);
  return `Datadog ${site?.label ?? drain.datadogSite ?? ''}`.trim();
}

// The server's rule for an HTTPS destination: https:// and port 443 only.
export function httpsUrlProblem(raw: string): string | undefined {
  const value = raw.trim();
  if (!value.toLowerCase().startsWith('https://')) return 'The URL must start with https://';
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    return 'The URL is not valid';
  }
  if (parsed.port !== '' && parsed.port !== '443') return 'Only port 443 is allowed';
  return undefined;
}

const drainsUrl = (projectId: string) => `/projects/${projectId}/log-drains`;
const drainsKey = (projectId: string) => ['log-drains', projectId];

export const listLogDrains = async (projectId: string): Promise<LogDrain[]> =>
  (await api.get<LogDrain[]>(drainsUrl(projectId))).data;

export const useLogDrains = (projectId: string, enabled: boolean) =>
  useQuery({
    queryKey: drainsKey(projectId),
    queryFn: () => listLogDrains(projectId),
    enabled: enabled && !!projectId,
  });

export const useCreateLogDrain = (projectId: string) => {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (input: CreateLogDrain) =>
      (await api.post<LogDrain>(drainsUrl(projectId), input)).data,
    onSuccess: () => client.invalidateQueries({ queryKey: drainsKey(projectId) }),
  });
};

export const useUpdateLogDrain = (projectId: string) => {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, changes }: { id: string; changes: UpdateLogDrain }) =>
      (await api.patch<LogDrain>(`${drainsUrl(projectId)}/${id}`, changes)).data,
    onSuccess: () => client.invalidateQueries({ queryKey: drainsKey(projectId) }),
  });
};

export const useDeleteLogDrain = (projectId: string) => {
  const client = useQueryClient();
  return useMutation({
    mutationFn: async (id: string) => {
      await api.delete(`${drainsUrl(projectId)}/${id}`);
    },
    onSuccess: () => client.invalidateQueries({ queryKey: drainsKey(projectId) }),
  });
};
