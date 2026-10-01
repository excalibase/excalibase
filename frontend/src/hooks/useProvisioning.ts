import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { useCallback } from 'react';
import { api } from '../api/client';
import { useSSE } from './useSSE';
import { PROJECT_FOLLOW_MS, RESPOND_ASYNC, settleProject } from './projectFollow';
import type { DatabaseInstance, DatabaseSettings, ProvisioningRequest, CredentialsResponse, BackupConfig, BackupInfo } from '../types';

export const useInstances = () => {
  return useQuery({
    queryKey: ['instances'],
    queryFn: async () => {
      const response = await api.get<DatabaseInstance[]>('/provision');
      return response.data;
    },
    // Removed polling - using SSE for real-time updates instead
    // Keep staleTime to allow refetching on mount/window focus
    staleTime: 30000, // Consider data fresh for 30 seconds
  });
};

export const useInstance = (projectId: string) => {
  return useQuery({
    queryKey: ['instance', projectId],
    queryFn: async () => {
      const response = await api.get<DatabaseInstance>(`/provision/${projectId}`);
      return response.data;
    },
    enabled: !!projectId,
    refetchInterval: 3000, // Poll more frequently for single instance
  });
};

/**
 * Real-time SSE hook for provisioning updates
 * Replaces polling with Server-Sent Events
 */
export const useInstanceSSE = (projectId: string) => {
  const queryClient = useQueryClient();

  const handleMessage = useCallback((instance: DatabaseInstance) => {
    // Update React Query cache with new data
    queryClient.setQueryData(['instance', projectId], instance);

    // Also update the instances list
    queryClient.setQueryData<DatabaseInstance[]>(['instances'], (old) => {
      if (!old) return [instance];
      const index = old.findIndex((i) => i.projectId === projectId);
      if (index === -1) return [...old, instance];
      const updated = [...old];
      updated[index] = instance;
      return updated;
    });
  }, [projectId, queryClient]);

  return useSSE<DatabaseInstance>(
    projectId ? `/provision/${projectId}/events` : null,
    handleMessage,
    { enabled: !!projectId },
  );
};

export const useCredentials = (projectId: string) => {
  return useQuery({
    queryKey: ['credentials', projectId],
    queryFn: async () => {
      const response = await api.get<CredentialsResponse>(`/provision/${projectId}/credentials`);
      return response.data;
    },
    enabled: !!projectId,
  });
};

export const useProvisionDatabase = () => {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (request: ProvisioningRequest) => {
      // A build outlasts one request: the answer comes once the project exists
      // (PROVISIONING) and the project page follows its status to ACTIVE or FAILED.
      const response = await api.post<DatabaseInstance>('/provision', request, {
        headers: { Prefer: 'respond-async' },
      });
      return response.data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['instances'] });
    },
  });
};

// A project holding data is stopped (backup first) before its grace period;
// the answer comes once the stop is recorded and the project is followed
// until it is PENDING_DELETION, or fails with the reason the server records.
export const useDeprovisionDatabase = (followMs = PROJECT_FOLLOW_MS) => {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async (projectId: string) => {
      const response = await api.delete<{ status?: string }>(`/provision/${projectId}`, RESPOND_ASYNC);
      return settleProject(projectId, 'deletion', response, followMs);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['instances'] });
    },
  });
};

export const useSetDeletionProtection = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ projectId, enabled }: { projectId: string; enabled: boolean }) => {
      await api.patch(`/provision/${projectId}/deletion-protection`, { enabled });
    },
    onSuccess: (_data, { projectId }) => {
      queryClient.invalidateQueries({ queryKey: ['project', projectId] });
      queryClient.invalidateQueries({ queryKey: ['instances'] });
    },
  });
};

export const useCancelDeletion = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (projectId: string) => {
      await api.post(`/provision/${projectId}/deletion/cancel`);
    },
    onSuccess: (_data, projectId) => {
      queryClient.invalidateQueries({ queryKey: ['project', projectId] });
      queryClient.invalidateQueries({ queryKey: ['instances'] });
    },
  });
};

interface PauseResponse {
  projectId: string;
  status: string;
  pauseReason?: string;
}

// Pause and resume answer once they are recorded (PAUSING, RESUMING) and are
// followed until the project is PAUSED or ACTIVE, or names why not.
export const usePauseProject = (followMs = PROJECT_FOLLOW_MS) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ projectId, reason }: { projectId: string; reason?: string }) => {
      const response = await api.post<PauseResponse>(
        `/provision/${projectId}/pause`,
        { reason: reason ?? 'manual' },
        RESPOND_ASYNC,
      );
      return settleProject(projectId, 'pause', response, followMs);
    },
    onSuccess: (_, vars) => {
      queryClient.invalidateQueries({ queryKey: ['instances'] });
      queryClient.invalidateQueries({ queryKey: ['instance', vars.projectId] });
      // SettingsPage queryKey is ['project', projectId] — invalidate that
      // too so the page repaints with the new status without a manual refresh.
      queryClient.invalidateQueries({ queryKey: ['project', vars.projectId] });
    },
  });
};

export const useResumeProject = (followMs = PROJECT_FOLLOW_MS) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (projectId: string) => {
      const response = await api.post<PauseResponse>(`/provision/${projectId}/resume`, undefined, RESPOND_ASYNC);
      return settleProject(projectId, 'resume', response, followMs);
    },
    onSuccess: (_, projectId) => {
      queryClient.invalidateQueries({ queryKey: ['instances'] });
      queryClient.invalidateQueries({ queryKey: ['instance', projectId] });
      queryClient.invalidateQueries({ queryKey: ['project', projectId] });
    },
  });
};

// UpgradeResponse is the project as the control plane holds it after the
// patch, not an outcome Studio assumed. The rolling restart it triggers is
// asynchronous, so `status` is what has actually been observed so far.
export interface UpgradeResponse {
  projectId: string;
  status: string;
  currentStage?: string;
  postgresVersion: string;
}

// useUpgradeMinorVersion moves a project onto the newest patch of the major it
// already runs. The major is not sent: the control plane reads it off the
// project, so no call from here can move a project between majors.
export const useUpgradeMinorVersion = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (projectId: string) => {
      const response = await api.post<UpgradeResponse>(`/provision/${projectId}/upgrade`);
      return response.data;
    },
    onSuccess: (_, projectId) => {
      queryClient.invalidateQueries({ queryKey: ['instances'] });
      queryClient.invalidateQueries({ queryKey: ['instance', projectId] });
      queryClient.invalidateQueries({ queryKey: ['project', projectId] });
    },
  });
};

export const useConfigureBackup = () => {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: async ({ projectId, config }: { projectId: string; config: BackupConfig }) => {
      const response = await api.post(`/provision/${projectId}/backup/configure`, config);
      return response.data;
    },
    onSuccess: (_, variables) => {
      queryClient.invalidateQueries({ queryKey: ['instance', variables.projectId] });
    },
  });
};

export const useTriggerBackup = () => {
  return useMutation({
    mutationFn: async (projectId: string) => {
      const response = await api.post(`/provision/${projectId}/backup/trigger`);
      return response.data;
    },
  });
};

export const useListBackups = (projectId: string) => {
  return useQuery({
    queryKey: ['backups', projectId],
    queryFn: async () => {
      const response = await api.get<{ backups: BackupInfo[]; backupEnabled: boolean; schedule: string; retentionDays: number }>(
        `/provision/${projectId}/backup/list`
      );
      return response.data;
    },
    enabled: !!projectId,
  });
};

export interface RestoreRequest {
  // Display name for the restored project. The project id is generated by
  // the platform and returned on the restore job.
  newProjectName: string;
  targetTime?: string;       // RFC 3339 with a zone, e.g. "2024-01-15T12:00:00.000Z" — leave blank for full restore
  backupId?: string;
}

export const useRestoreFromBackup = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (request: RestoreRequest) => {
      const response = await api.post<Record<string, string>>(
        `/provision/${projectId}/backup/restore`, request
      );
      return response.data;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['instances'] });
    },
  });
};

export const useLogs = (projectId: string, lines: number = 100) => {
  return useQuery({
    queryKey: ['logs', projectId, lines],
    queryFn: async () => {
      const response = await api.get<string>(`/provision/${projectId}/logs?lines=${lines}`);
      return response.data;
    },
    enabled: !!projectId,
    refetchInterval: 15000,
  });
};

// Adds the database to a project created without one. The answer is the
// project as the add left it: with its database, or still without one and
// the failure named, so the caller can show it and let the admin retry.
export const useAddDatabase = (projectId: string) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (settings: DatabaseSettings) => {
      const response = await api.post<DatabaseInstance>(`/provision/${projectId}/database`, settings);
      return response.data;
    },
    onSettled: () => {
      queryClient.invalidateQueries({ queryKey: ['instances'] });
      queryClient.invalidateQueries({ queryKey: ['instance', projectId] });
      queryClient.invalidateQueries({ queryKey: ['project', projectId] });
    },
  });
};
