import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from './client';

// The browser origins a project's API answers (EXC-23). Add and remove change
// one origin under the server's row lock, so a concurrent edit is never lost.
export interface ProjectCors {
  allowedOrigins: string[];
  allowWildcard: boolean;
}

const corsKey = (projectId: string) => ['project-cors', projectId];

export const useProjectCors = (projectId: string) =>
  useQuery({
    queryKey: corsKey(projectId),
    queryFn: async () => (await api.get<ProjectCors>(`/projects/${projectId}/cors`)).data,
    enabled: !!projectId,
    retry: false,
  });

function useCorsChange<T>(projectId: string, change: (input: T) => Promise<ProjectCors>) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: change,
    onSuccess: (cors) =>
      queryClient.setQueryData<ProjectCors>(corsKey(projectId), {
        allowedOrigins: cors.allowedOrigins,
        allowWildcard: cors.allowWildcard,
      }),
  });
}

export const useAddCorsOrigin = (projectId: string) =>
  useCorsChange(projectId, async (origin: string) =>
    (await api.post<ProjectCors>(`/projects/${projectId}/cors/origins`, { origin })).data,
  );

export const useRemoveCorsOrigin = (projectId: string) =>
  useCorsChange(projectId, async (origin: string) =>
    (await api.delete<ProjectCors>(`/projects/${projectId}/cors/origins`, { params: { origin } })).data,
  );

// The wildcard is only ever replaced as a whole list.
export const useClearCorsWildcard = (projectId: string) =>
  useCorsChange(projectId, async (_: void) =>
    (await api.put<ProjectCors>(`/projects/${projectId}/cors`, { allowedOrigins: [], allowWildcard: false })).data,
  );
