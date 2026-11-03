import { useQuery, keepPreviousData } from '@tanstack/react-query';
import { api } from '../api/client';
import { requestChartsPath, type RequestCharts, type RequestRange } from '../utils/requestCharts';

export const useRequestCharts = (projectId: string, range: RequestRange) =>
  useQuery({
    queryKey: ['request-charts', projectId, range],
    queryFn: async () => (await api.get<RequestCharts>(requestChartsPath(projectId, range))).data,
    enabled: !!projectId,
    refetchInterval: 60_000,
    placeholderData: keepPreviousData,
  });
