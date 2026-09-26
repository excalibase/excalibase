import { useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../../api/client';
import { apiErrorMessage } from '../../api/apps';

export interface AppLogLine {
  pod: string;
  time: string;
  text: string;
}

interface AppLogsPage {
  lines: AppLogLine[];
  cursor?: string;
}

const MAX_KEPT_LINES = 2000;

export const fetchAppLogs = async (
  projectId: string,
  appId: string,
  since?: string,
): Promise<AppLogsPage> =>
  (
    await api.get<AppLogsPage>(`/projects/${projectId}/apps/${appId}/logs`, {
      params: since ? { since } : {},
    })
  ).data;

// Polls with the server's cursor, so each answer holds only lines not shown yet.
export function AppLogs({
  projectId,
  appId,
  pollIntervalMs = 2000,
}: {
  readonly projectId: string;
  readonly appId: string;
  readonly pollIntervalMs?: number;
}) {
  const [lines, setLines] = useState<AppLogLine[]>([]);
  const cursor = useRef<string | undefined>(undefined);
  const { error, isFetched } = useQuery({
    queryKey: ['apps', projectId, appId, 'logs'],
    queryFn: async () => {
      const page = await fetchAppLogs(projectId, appId, cursor.current);
      cursor.current = page.cursor ?? cursor.current;
      setLines((kept) => [...kept, ...page.lines].slice(-MAX_KEPT_LINES));
      return page.cursor ?? null;
    },
    refetchInterval: pollIntervalMs,
    enabled: !!projectId && !!appId,
  });

  return (
    <section className="space-y-2" data-testid="app-logs">
      <h4 className="text-sm font-semibold text-text-primary">Logs</h4>
      {error && (
        <div role="alert" className="text-sm text-red-400">
          {apiErrorMessage(error, 'Could not read the logs')}
        </div>
      )}
      <div className="bg-black/60 border border-border-primary rounded-lg p-3 max-h-96 overflow-auto font-mono text-xs">
        {isFetched && !error && lines.length === 0 && (
          <p className="text-text-tertiary" data-testid="app-logs-empty">
            No output yet.
          </p>
        )}
        {lines.map((line) => (
          <div
            key={`${line.pod}-${line.time}-${line.text}`}
            className="whitespace-pre-wrap break-all"
          >
            <span className="text-text-tertiary">{new Date(line.time).toLocaleTimeString()} </span>
            <span className="text-purple-400">{line.pod} </span>
            <span className="text-text-primary">{line.text}</span>
          </div>
        ))}
      </div>
    </section>
  );
}
