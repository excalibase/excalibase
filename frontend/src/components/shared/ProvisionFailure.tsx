import type { ReactNode } from 'react';
import { useAuthStore } from '../../stores/auth-store';
import { plainProvisionFailure } from '../../utils/provisionFailure';

interface ProvisionFailureProps {
  readonly reason?: string;
  // Extra raw material (a rollback log); shown to a platform admin only.
  readonly detail?: ReactNode;
  readonly className?: string;
}

// A failed provisioning in customer words. The server's raw text sits in a
// collapsed block for a platform admin and is never shown to anyone else.
export function ProvisionFailure({ reason, detail, className = 'text-xs text-text-secondary break-words' }: ProvisionFailureProps) {
  const admin = useAuthStore((s) => s.user?.role === 'platform_admin');
  const showTechnical = admin && (Boolean(reason) || Boolean(detail));
  return (
    <div>
      <p className={className}>{plainProvisionFailure(reason)}</p>
      {showTechnical && (
        <details className="mt-2">
          <summary className="text-xs text-text-tertiary cursor-pointer hover:text-text-secondary">Technical details</summary>
          {reason && <p className="mt-1 text-xs text-text-secondary font-mono break-words">{reason}</p>}
          {detail}
        </details>
      )}
    </div>
  );
}
