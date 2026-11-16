import { useState } from 'react';
import { KeyRound } from 'lucide-react';
import { useResetPassword } from '../hooks/useProvisioning';
import { useFeatureEnabled } from '../hooks/useDeploymentMode';
import { useProjectRole } from '../hooks/useProjectRole';
import { passwordProblem } from '../utils/databasePassword';
import { serverErrorMessage } from '../utils/serverError';
import { Button } from './Button';
import { DatabasePasswordField } from './DatabasePasswordField';
import { ProjectPasswordPanel } from './ProjectPasswordPanel';

type Step = { kind: 'closed' } | { kind: 'asking' } | { kind: 'shown'; password: string };

// Resets the database password: the customer types a new one or leaves it
// blank for a generated one, and sees the result once in the one-time panel.
export function ResetPasswordButton({ projectId }: { readonly projectId: string }) {
  const { enabled } = useFeatureEnabled('onetimecredentials');
  const { canAdmin } = useProjectRole(projectId);
  const reset = useResetPassword(projectId);
  const [step, setStep] = useState<Step>({ kind: 'closed' });
  const [chosen, setChosen] = useState('');
  const [problem, setProblem] = useState<string | undefined>();

  if (!enabled || !canAdmin) return null;

  const close = () => {
    reset.reset();
    setChosen('');
    setProblem(undefined);
    setStep({ kind: 'closed' });
  };

  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    const found = passwordProblem(chosen) ?? undefined;
    setProblem(found);
    if (found) return;
    try {
      const answer = await reset.mutateAsync(chosen);
      reset.reset();
      setChosen('');
      setStep({ kind: 'shown', password: answer.password });
    } catch {
      // shown below from reset.error
    }
  };

  if (step.kind === 'shown') {
    return (
      <div className="basis-full">
        <ProjectPasswordPanel projectId={projectId} password={step.password} onDone={close} />
      </div>
    );
  }

  return (
    <>
      <Button variant="secondary" size="sm" onClick={() => setStep({ kind: 'asking' })}>
        <KeyRound className="w-4 h-4 mr-1.5" /> Reset password
      </Button>
      {step.kind === 'asking' && (
        <div role="dialog" aria-modal="true" aria-label="Reset password" className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4">
          <form onSubmit={(event) => void submit(event)} className="w-full max-w-md rounded-xl border border-border-primary bg-surface-card p-6 space-y-4">
            <h3 className="font-semibold text-text-primary">Reset password</h3>
            <p className="text-sm text-text-secondary">
              The current password stops working. Anything using it must be updated. The new password is shown once.
            </p>
            <DatabasePasswordField id="reset-password" label="New password (optional)" value={chosen} onChange={setChosen} error={problem} />
            {reset.error != null && (
              <p role="alert" className="text-xs text-color-error">{serverErrorMessage(reset.error, 'The password was not reset')}</p>
            )}
            <div className="flex justify-end gap-3">
              <Button type="button" variant="secondary" onClick={close}>Cancel</Button>
              <Button type="submit" disabled={reset.isPending}>Reset</Button>
            </div>
          </form>
        </div>
      )}
    </>
  );
}
