import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { getOverview, listConsoleOrgs, listConsoleUsers, suspendUser, unsuspendUser, type ListQuery } from '../api/adminConsole';

export const useOverview = () => useQuery({ queryKey: ['admin', 'console', 'overview'], queryFn: getOverview, staleTime: 15_000 });

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
