import { Navigate, Outlet, useLocation } from 'react-router-dom';
import { Loader2 } from 'lucide-react';
import { useVaultStatus } from '../../hooks/useVault';
import { useSetupStatus } from '../../hooks/useSetup';

/**
 * Gates the entire app on first-run state. Redirects to /setup until all
 * three conditions are met:
 *   1. vault initialized
 *   2. vault unsealed
 *   3. at least one platform_admin user exists
 *
 * The /setup route itself is excluded from the gate so the user can
 * complete the wizard. Mirrors HashiCorp Vault's init → unseal → ready
 * flow, extended with admin-creation as the final step.
 */
export function VaultGuard() {
  const { data: vault, isLoading: vaultLoading } = useVaultStatus();
  const { data: setup, isLoading: setupLoading } = useSetupStatus();
  const location = useLocation();

  if (vaultLoading || setupLoading || !vault || !setup) {
    return (
      <div className="min-h-screen flex items-center justify-center bg-bg-primary">
        <Loader2 className="w-6 h-6 animate-spin text-purple-400" />
      </div>
    );
  }

  const needsSetup = !vault.initialized || vault.sealed || !setup.hasAdmin;
  const onSetupRoute = location.pathname === '/setup';

  if (needsSetup && !onSetupRoute) {
    return <Navigate to="/setup" replace />;
  }
  if (!needsSetup && onSetupRoute) {
    return <Navigate to="/" replace />;
  }

  return <Outlet />;
}
