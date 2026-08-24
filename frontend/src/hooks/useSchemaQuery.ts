import { useMutation } from '@tanstack/react-query';
import { api } from '../api/client';
import type { QueryResult } from '../types/schema';

export function useExecuteQuery(projectId: string) {
  return useMutation({
    mutationFn: async (query: string) => {
      const res = await api.post<QueryResult>(`/schema/${projectId}/query`, { query });
      return res.data;
    },
  });
}
