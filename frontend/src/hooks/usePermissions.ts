import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  deleteFunctionPermission,
  deleteTablePermission,
  getPermissionDocument,
  putFunctionPermission,
  putTablePermission,
  trackFunction,
  untrackFunction,
  type AnyPermission,
  type FunctionPermission,
  type Operation,
  type PermissionByOperation,
  type TrackFunctionRequest,
  type TrackedFunctionAnswer,
} from '../api/permissions';

// A project's API permissions. Errors are left to the caller, which shows the
// server's reason next to the control that failed.

export const permissionsKey = (projectId: string) => ['permissions', projectId] as const;

/** enabled is false for callers below Developer: the server answers them 403. */
export function usePermissionDocument(projectId: string, enabled: boolean) {
  return useQuery({
    queryKey: permissionsKey(projectId),
    queryFn: () => getPermissionDocument(projectId),
    enabled: !!projectId && enabled,
    staleTime: 10_000,
  });
}

function useRefreshingMutation<TVars, TResult>(projectId: string, mutationFn: (vars: TVars) => Promise<TResult>) {
  const qc = useQueryClient();
  return useMutation<TResult, Error, TVars>({
    mutationFn,
    onSuccess: () => qc.invalidateQueries({ queryKey: permissionsKey(projectId) }),
  });
}

export interface TablePermissionTarget {
  table: string;
  role: string;
  operation: Operation;
}

export interface SaveTablePermissionVars extends TablePermissionTarget {
  permission: AnyPermission;
}

export function useSaveTablePermission(projectId: string) {
  return useRefreshingMutation(projectId, ({ table, role, operation, permission }: SaveTablePermissionVars) =>
    putTablePermission(projectId, table, role, operation, permission as PermissionByOperation[typeof operation]),
  );
}

export function useDeleteTablePermission(projectId: string) {
  return useRefreshingMutation(projectId, ({ table, role, operation }: TablePermissionTarget) =>
    deleteTablePermission(projectId, table, role, operation),
  );
}

export function useTrackFunction(projectId: string) {
  return useRefreshingMutation<TrackFunctionRequest, TrackedFunctionAnswer>(projectId, (request) =>
    trackFunction(projectId, request),
  );
}

export function useUntrackFunction(projectId: string) {
  return useRefreshingMutation(projectId, (fn: string) => untrackFunction(projectId, fn));
}

export function useAddFunctionPermission(projectId: string) {
  return useRefreshingMutation(projectId, ({ function: fn, role }: FunctionPermission) =>
    putFunctionPermission(projectId, fn, role),
  );
}

export function useRemoveFunctionPermission(projectId: string) {
  return useRefreshingMutation(projectId, ({ function: fn, role }: FunctionPermission) =>
    deleteFunctionPermission(projectId, fn, role),
  );
}
