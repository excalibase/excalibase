import { useApps } from '../api/apps';
import { useAppHostingEnabled } from './useDeploymentMode';

// Whether the project runs apps beside its database; only then is the
// database's internal address worth showing, for those apps.
export function useHostsApps(projectId: string): boolean {
  const { enabled } = useAppHostingEnabled();
  const { data } = useApps(projectId, enabled);
  return Array.isArray(data) && data.length > 0;
}
