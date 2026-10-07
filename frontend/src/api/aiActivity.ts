import { useMutation, useQuery } from '@tanstack/react-query';
import { api } from './client';

// One call an AI tool made through MCP, as GET /api/projects/{id}/ai-activity/
// lists it. tokenId is set while the token exists and the caller may revoke
// it: their own, or any member's for an org owner or admin.
export interface AiActivityCall {
  id: number;
  tool: string;
  status: 'ok' | 'error';
  httpStatus?: number;
  tokenName: string;
  userId: string;
  at: string;
  mine: boolean;
  tokenId?: string;
  tokenRevoked?: boolean;
}

export const aiActivityKey = (projectId: string) => ['ai-activity', projectId];

export function useAiActivity(projectId: string) {
  return useQuery({
    queryKey: aiActivityKey(projectId),
    queryFn: async () =>
      (await api.get<{ calls: AiActivityCall[] }>(`/projects/${encodeURIComponent(projectId)}/ai-activity/`)).data.calls,
    enabled: !!projectId,
  });
}

// Revokes a token the project's feed shows.
export function useRevokeActivityToken(projectId: string) {
  return useMutation({
    mutationFn: async (tokenId: string) => {
      await api.delete(`/projects/${encodeURIComponent(projectId)}/ai-activity/tokens/${encodeURIComponent(tokenId)}`);
    },
  });
}

export function callResult(call: AiActivityCall): string {
  if (call.status === 'ok') return 'OK';
  if (call.httpStatus === 401 || call.httpStatus === 403 || call.httpStatus === 404) return `Refused (${call.httpStatus})`;
  return call.httpStatus ? `Failed (${call.httpStatus})` : 'Failed';
}
