import { useQuery } from '@tanstack/react-query';
import { api } from '../api/client';
import type { AdvisorFinding } from '../types/schema';

export function usePerformanceAdvisor(projectId: string, schema = 'public') {
  return useQuery({
    queryKey: ['advisor-performance', projectId, schema],
    queryFn: async () => {
      const res = await api.get<AdvisorFinding[]>(`/schema/${projectId}/advisors/performance`, { params: { schema } });
      return res.data;
    },
    enabled: !!projectId,
    staleTime: 60000,
  });
}

export function useSecurityAdvisor(projectId: string, schema = 'public') {
  return useQuery({
    queryKey: ['advisor-security', projectId, schema],
    queryFn: async () => {
      const res = await api.get<AdvisorFinding[]>(`/schema/${projectId}/advisors/security`, { params: { schema } });
      return res.data;
    },
    enabled: !!projectId,
    staleTime: 60000,
  });
}
