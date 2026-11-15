import { useApps } from '../api/apps';
import { useAppHostingEnabled } from './useDeploymentMode';

// Whether the project runs apps beside its database; only then is the
// DATABASE_URL note worth showing.
export function useHostsApps(projectId: string): boolean {
  const { enabled } = useAppHostingEnabled();
  const { data } = useApps(projectId, enabled);
  return Array.isArray(data) && data.length > 0;
}
