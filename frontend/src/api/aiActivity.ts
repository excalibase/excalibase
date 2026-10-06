import { useQuery } from '@tanstack/react-query';
import { api } from './client';

// One call an AI tool made through MCP, as GET /api/projects/{id}/ai-activity/
// lists it. tokenId is set only for the caller's own token while it exists.
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

export function callResult(call: AiActivityCall): string {
  if (call.status === 'ok') return 'OK';
  if (call.httpStatus === 401 || call.httpStatus === 403 || call.httpStatus === 404) return `Refused (${call.httpStatus})`;
  return call.httpStatus ? `Failed (${call.httpStatus})` : 'Failed';
}
