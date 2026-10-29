import { useEffect, useRef } from 'react';
import { useMutation, useQuery, useQueryClient, type QueryClient } from '@tanstack/react-query';
import { api } from '../api/client';
import type { MajorUpgradeInfo, MajorUpgradeRun } from '../types';

export const MAJOR_UPGRADE_POLL_MS = 5_000;

const majorUpgradeKey = (projectId: string) => ['major-upgrade', projectId] as const;

function refreshProject(queryClient: QueryClient, projectId: string) {
  queryClient.invalidateQueries({ queryKey: ['project', projectId] });
  queryClient.invalidateQueries({ queryKey: ['instance', projectId] });
  queryClient.invalidateQueries({ queryKey: ['instances'] });
}

// What the project can upgrade to and its latest run. Polls while a run is
// RUNNING; when it ends the project status (UPGRADING) is re-read too.
export function useMajorUpgrade(projectId: string) {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: majorUpgradeKey(projectId),
    queryFn: async () => (await api.get<MajorUpgradeInfo>(`/provision/${projectId}/major-upgrade`)).data,
    enabled: !!projectId,
    refetchInterval: (current) =>
      current.state.data?.latest?.status === 'RUNNING' ? MAJOR_UPGRADE_POLL_MS : false,
  });

  const status = query.data?.latest?.status;
  const previousStatus = useRef(status);
  useEffect(() => {
    if (previousStatus.current === 'RUNNING' && status !== 'RUNNING') refreshProject(queryClient, projectId);
    previousStatus.current = status;
  }, [status, projectId, queryClient]);

  return query;
}

export function useStartMajorUpgrade(projectId: string) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (targetVersion: string) =>
      (
        await api.post<{ upgrade: MajorUpgradeRun }>(`/provision/${projectId}/major-upgrade`, {
          targetVersion,
          confirm: true,
        })
      ).data.upgrade,
    onSuccess: (upgrade) => {
      // Show the run at once; the RUNNING status starts the poll.
      queryClient.setQueryData<MajorUpgradeInfo>(majorUpgradeKey(projectId), (current) =>
        current ? { ...current, latest: upgrade } : current,
      );
      refreshProject(queryClient, projectId);
    },
    // A refusal (a run already going, a blocker since) means the card is stale.
    onError: () => {
      void queryClient.invalidateQueries({ queryKey: majorUpgradeKey(projectId) });
    },
  });
}
