import type { ReactNode } from 'react';
import { useAuthStore } from '../../stores/auth-store';
import { plainProvisionFailure } from '../../utils/provisionFailure';

interface ProvisionFailureProps {
  readonly reason?: string;
  // The server's plain failureKind; a customer's answer carries it in place of the text.
  readonly kind?: string;
  // One of the server's own customer-safe sentences (failureMessage); shown as it is.
  readonly message?: string;
  // Extra raw material (a rollback log); shown to a platform admin only.
  readonly detail?: ReactNode;
  readonly className?: string;
}

// A failed provisioning in customer words. The server's raw text sits in a
// collapsed block for a platform admin and is never shown to anyone else.
export function ProvisionFailure({ reason, kind, message, detail, className = 'text-xs text-text-secondary break-words' }: ProvisionFailureProps) {
  const admin = useAuthStore((s) => s.user?.role === 'platform_admin' || s.user?.role === 'platform_operator');
  const showTechnical = admin && (Boolean(reason) || Boolean(detail));
  return (
    <div>
      <p className={className}>{message ?? plainProvisionFailure(reason, kind)}</p>
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
