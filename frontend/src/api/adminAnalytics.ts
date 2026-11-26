import { api } from './client';

// The analytics tabs' wire types (EXC-898). They mirror internal/adminconsole/analytics.go.

export interface DayCount {
  day: string;
  total: number;
  // Suspended sign-ups, or the distinct users behind a day's refusals; absent when zero.
  flagged?: number;
}

export interface FunnelStep {
  label: string;
  count: number;
}

export interface DomainStat {
  domain: string;
  accounts: number;
  suspended: number;
}

export interface UserAnalytics {
  windowDays: number;
  signups: number;
  suspended: number;
  activeLast7Days: number;
  perDay: DayCount[];
  funnel: FunnelStep[];
  domains: DomainStat[];
}

export interface RefusalBrief {
  at: string;
  projectId: string;
  userId: string;
  email: string;
  kind: 'proxy' | 'registry';
  details: string;
  suspended: boolean;
}

export interface AbuseAnalytics {
  windowDays: number;
  perDay: DayCount[];
  proxyRefusals: number;
  registryRefusals: number;
  refusedUsers: number;
  refusedStillActive: number;
  suspended: number;
  latest: RefusalBrief[];
}

export interface TrafficAnalytics {
  windowDays: number;
  peakConnectionsPerDay: DayCount[];
  peakConnections: number;
}

export const getUserAnalytics = async () => (await api.get<UserAnalytics>('/admin/analytics/users')).data;
export const getAbuseAnalytics = async () => (await api.get<AbuseAnalytics>('/admin/analytics/abuse')).data;
export const getTrafficAnalytics = async () => (await api.get<TrafficAnalytics>('/admin/analytics/traffic')).data;
