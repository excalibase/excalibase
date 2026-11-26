import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { getFailedProjects, getOverview, getSuspensions, listConsoleOrgs, listConsoleUsers, suspendUser, unsuspendUser, type ListQuery } from '../api/adminConsole';

export const useOverview = () => useQuery({ queryKey: ['admin', 'console', 'overview'], queryFn: getOverview, staleTime: 15_000 });

// The needs-attention lists load a page at a time as the admin scrolls.
const attentionList = <T,>(key: string, read: (cursor?: string) => Promise<{ items: T[]; total: number; nextCursor?: string }>) =>
  ({
    queryKey: ['admin', 'console', 'attention', key],
    queryFn: ({ pageParam }: { pageParam: string | undefined }) => read(pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: { nextCursor?: string }) => last.nextCursor || undefined,
    staleTime: 15_000,
  });

export const useSuspensions = () => useInfiniteQuery(attentionList('suspensions', getSuspensions));
export const useFailedProjects = () => useInfiniteQuery(attentionList('failed-projects', getFailedProjects));

export const useConsoleUsers = (query: ListQuery) =>
  useQuery({
    queryKey: ['admin', 'console', 'users', query],
    queryFn: () => listConsoleUsers(query),
    placeholderData: keepPreviousData,
    staleTime: 10_000,
  });

export const useConsoleOrgs = (query: ListQuery) =>
  useQuery({
    queryKey: ['admin', 'console', 'orgs', query],
    queryFn: () => listConsoleOrgs(query),
    placeholderData: keepPreviousData,
    staleTime: 10_000,
  });

// Suspending or unsuspending changes what every console table says, so each
// one is read again.
export const useAccountAction = (action: 'suspend' | 'unsuspend') => {
  const client = useQueryClient();
  return useMutation({
    mutationFn: ({ userId, reason, reasonCode }: { userId: string; reason: string; reasonCode?: string }) =>
      action === 'suspend' ? suspendUser(userId, reason, reasonCode) : unsuspendUser(userId, reason),
    onSuccess: () => {
      client.invalidateQueries({ queryKey: ['admin'] });
      client.invalidateQueries({ queryKey: ['instances'] });
    },
  });
};
