import { keepPreviousData, useInfiniteQuery, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { getFailedProjects, getOverview, getSuspensions, getUserDetail, listAudit, listConsoleOrgs, listConsoleUsers, suspendUser, unsuspendUser, type AuditFilter, type AuditPage, type ListQuery } from '../api/adminConsole';

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

// The audit log loads a page at a time; the cursor is the server's keyset token.
export const useAuditLog = (filter: AuditFilter) =>
  useInfiniteQuery({
    queryKey: ['admin', 'audit', filter],
    queryFn: ({ pageParam }: { pageParam: string | undefined }) => listAudit(filter, pageParam),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last: AuditPage) => last.nextCursor || undefined,
    staleTime: 10_000,
  });

export const useUserDetail = (userId: string) =>
  useQuery({ queryKey: ['admin', 'account', userId], queryFn: () => getUserDetail(userId), staleTime: 10_000 });
