import { Navigate } from 'react-router-dom';
import { useAdminRouteRule } from '../../hooks/useDeploymentMode';

// Wraps the console's route: on a host that is not the configured admin host
// the page does not exist, whoever is signed in.
export function AdminRouteGuard({ children }: { readonly children: React.ReactNode }) {
  const rule = useAdminRouteRule();
  if (rule === 'loading') return null;
  if (rule === 'refused') return <Navigate to="/" replace />;
  return <>{children}</>;
}
