import { useState } from 'react';
import { useAdminCreditChange } from '../../../api/credits';
import { dollars, microsFromDollars } from '../../../pages/usage/creditsFormat';
import { serverErrorMessage } from '../../../utils/serverError';
import { ReasonDialog } from '../ReasonDialog';

const MAX_ACTION_MICROS = 1_000_000_000;
const FIELD = 'mt-1 w-full rounded-lg bg-bg-secondary border border-border-primary p-2 text-text-primary';

interface GrantCreditDialogProps {
  readonly orgId: string;
  readonly orgSlug: string;
  readonly onDone: () => void;
  readonly onCancel: () => void;
}

// One admin action on an org's credit: add some or take some back. A new key per opened dialog makes a retried click one entry.
export function GrantCreditDialog({ orgId, orgSlug, onDone, onCancel }: GrantCreditDialogProps) {
  const change = useAdminCreditChange();
  const [key] = useState(() => `grant-${crypto.randomUUID()}`);
  const [direction, setDirection] = useState<'add' | 'take'>('add');
  const [amount, setAmount] = useState('');
  const [expiry, setExpiry] = useState('');
  const micros = microsFromDollars(amount);
  const overCap = micros !== null && micros > MAX_ACTION_MICROS;
  const problem = amount !== '' && (micros === null ? 'Enter an amount like 25 or 12.50.' : overCap && `One action may move at most ${dollars(MAX_ACTION_MICROS)}.`);

  const confirm = async (reason: string) => {
    if (micros === null || overCap) return;
    try {
      await change.mutateAsync({
        orgId, key, reason, amountMicros: direction === 'add' ? micros : -micros,
        expiresAt: direction === 'add' && expiry ? new Date(`${expiry}T00:00:00Z`).toISOString() : undefined,
      });
      onDone();
    } catch {
      // the dialog stays open and shows change.error
    }
  };

  return (
    <ReasonDialog
      title={`Credit for "${orgSlug}"`}
      description="Adds credit to the organization or takes some back. Written to the audit log in the same step."
      confirmLabel={direction === 'add' ? 'Add credit' : 'Take back credit'}
      danger={direction === 'take'}
      busy={change.isPending || micros === null || overCap}
      error={change.error ? serverErrorMessage(change.error, 'The credit change failed') : undefined}
      onConfirm={confirm}
      onCancel={onCancel}
    >
      <div className="space-y-3">
        <label className="block text-sm text-text-secondary">
          <span>Action</span>
          <select className={FIELD} value={direction} onChange={(event) => setDirection(event.target.value as 'add' | 'take')}>
            <option value="add">Add credit</option>
            <option value="take">Take back credit</option>
          </select>
        </label>
        <label className="block text-sm text-text-secondary">
          <span>Amount in dollars</span>
          <input className={FIELD} inputMode="decimal" value={amount} onChange={(event) => setAmount(event.target.value)} />
        </label>
        {problem && <p role="alert" className="text-sm text-red-400">{problem}</p>}
        {direction === 'add' && (
          <label className="block text-sm text-text-secondary">
            <span>Expires on (UTC, optional)</span>
            <input className={FIELD} type="date" value={expiry} onChange={(event) => setExpiry(event.target.value)} />
          </label>
        )}
      </div>
    </ReasonDialog>
  );
}
