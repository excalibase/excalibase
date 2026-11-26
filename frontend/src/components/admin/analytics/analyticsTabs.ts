// Tab ids of the analytics tabs in the admin console's bar.
export const ANALYTICS_TABS = {
  users: 'analytics-users',
  abuse: 'analytics-abuse',
  traffic: 'analytics-traffic',
  storage: 'analytics-storage',
} as const;

export const isAnalyticsTab = (id: string) => (Object.values(ANALYTICS_TABS) as string[]).includes(id);
