import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';
import type { CredentialsResponse } from '../types';

export const CREDENTIALS_POLL_CAP_MS = 15 * 60_000;
const FIRST_DELAY_MS = 3_000;
const LONGEST_DELAY_MS = 15_000;

// A project in one of these will not get a login by waiting: its build
// failed, or it is being or about to be removed.
const NO_LOGIN_COMING = new Set(['FAILED', 'DELETING', 'PENDING_DELETION', 'BACKUPS_PENDING_DELETE']);
// Answers that waiting does not change: not allowed to see it, gone, or being
// deleted or restored. A 503 is the build still going.
const FINAL_ANSWERS = new Set([403, 404, 409]);

function statusOf(error: unknown): number | undefined {
  return (error as { response?: { status?: number } } | null)?.response?.status;
}

// The credentials of a project that may still be building: asks again with a
// growing pause until they answer, then stops. It stops too on an answer
// waiting cannot change, for a project that will not get a login, and after a
// time cap, and says so with gaveUp.
export function useCredentialsWhenReady(projectId: string, projectStatus?: string) {
  const [capped, setCapped] = useState(false);
  useEffect(() => {
    const timer = setTimeout(() => setCapped(true), CREDENTIALS_POLL_CAP_MS);
    return () => clearTimeout(timer);
  }, [projectId]);

  const noLoginComing = projectStatus !== undefined && NO_LOGIN_COMING.has(projectStatus);
  const query = useQuery({
    queryKey: ['credentials', projectId],
    queryFn: async () => (await api.get<CredentialsResponse>(`/provision/${projectId}/credentials`)).data,
    enabled: !!projectId && !capped && !noLoginComing,
    retry: false,
    refetchOnWindowFocus: false,
    refetchInterval: (current) => {
      if (current.state.data) return false;
      if (FINAL_ANSWERS.has(statusOf(current.state.error) ?? 0)) return false;
      return Math.min(FIRST_DELAY_MS * 1.5 ** current.state.errorUpdateCount, LONGEST_DELAY_MS);
    },
  });

  const credentials = query.data;
  const finalAnswer = FINAL_ANSWERS.has(statusOf(query.error) ?? 0);
  return { credentials, gaveUp: !credentials && (capped || noLoginComing || finalAnswer) };
}
