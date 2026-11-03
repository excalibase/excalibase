import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { toast } from 'sonner';
import { endUserName, resetEndUserMFA, type EndUser } from '../../api/endUsers';
import { apiErrorMessage } from '../../api/permissions';
import { ConfirmModal } from '../ui/ConfirmModal';

// An admin's MFA reset for one end user (EXC-662, flag `authmfa`), for a user
// who lost both their authenticator app and their recovery codes.
export function ResetEndUserMFA({
  projectId,
  user,
}: Readonly<{ projectId: string; user: Pick<EndUser, 'id' | 'email'> }>) {
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const name = endUserName(user);
  const reset = useMutation({
    mutationFn: () => resetEndUserMFA(projectId, user.id),
    onSuccess: () => toast.success(`MFA of ${name} was reset`),
    onError: (err) => setError(apiErrorMessage(err, 'MFA could not be reset.')),
  });

  return (
    <>
      <button
        type="button"
        aria-label={`Reset MFA of ${name}`}
        onClick={() => setConfirming(true)}
        className="px-2 py-1 text-xs rounded text-amber-400 hover:bg-amber-500/10"
      >
        Reset MFA
      </button>
      {error && (
        <p role="alert" className="text-xs text-red-400 mt-1">
          {error}
        </p>
      )}
      <ConfirmModal
        open={confirming}
        onClose={() => setConfirming(false)}
        onConfirm={() => {
          setConfirming(false);
          setError(null);
          return reset.mutateAsync().catch(() => undefined);
        }}
        title="Reset this user's MFA?"
        message={`${name} loses every authenticator app and recovery code and is signed out everywhere. They sign in again with their first factor and can add a new app.`}
        confirmLabel="Reset MFA"
        destructive
        loading={reset.isPending}
      />
    </>
  );
}
