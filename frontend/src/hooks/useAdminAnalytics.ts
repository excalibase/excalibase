import { useQuery } from '@tanstack/react-query';
import { getAbuseAnalytics, getTrafficAnalytics, getUserAnalytics } from '../api/adminAnalytics';

// Each read is asked for only while the analytics tabs are on, so a server with
// the flag off is never called (it would answer 404).
const options = (enabled: boolean) => ({ enabled, staleTime: 60_000, retry: false });

export const useUserAnalytics = (enabled: boolean) =>
  useQuery({ queryKey: ['admin', 'analytics', 'users'], queryFn: getUserAnalytics, ...options(enabled) });

export const useAbuseAnalytics = (enabled: boolean) =>
  useQuery({ queryKey: ['admin', 'analytics', 'abuse'], queryFn: getAbuseAnalytics, ...options(enabled) });

export const useTrafficAnalytics = (enabled: boolean) =>
  useQuery({ queryKey: ['admin', 'analytics', 'traffic'], queryFn: getTrafficAnalytics, ...options(enabled) });
