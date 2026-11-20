import { useState } from 'react';
import { Button } from '../Button';

// An admin action on a customer's data or account is logged with a written
// reason (EXC-757), so every such action asks for one first.
export const MIN_REASON_LENGTH = 8;

interface ReasonDialogProps {
  readonly title: string;
  readonly confirmLabel: string;
  readonly description?: string;
  readonly busy?: boolean;
  readonly error?: string;
  readonly danger?: boolean;
  readonly onConfirm: (reason: string) => void;
  readonly onCancel: () => void;
  // Extra fields the action needs besides the reason, such as a plan choice.
  readonly children?: React.ReactNode;
}

export function ReasonDialog({ title, confirmLabel, description, busy, error, danger, onConfirm, onCancel, children }: ReasonDialogProps) {
  const [reason, setReason] = useState('');
  const trimmed = reason.trim();
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
      <div role="dialog" aria-modal="true" aria-label={title} className="w-full max-w-md bg-surface-card border border-border-primary rounded-xl p-6 space-y-4">
        <h3 className="text-lg font-semibold">{title}</h3>
        {description && <p className="text-sm text-text-secondary">{description}</p>}
        {children}
        <label className="block text-sm text-text-secondary">
          Reason
          <textarea
            className="mt-1 w-full rounded-lg bg-bg-secondary border border-border-primary p-2 text-text-primary"
            rows={3}
            value={reason}
            maxLength={500}
            onChange={(event) => setReason(event.target.value)}
          />
        </label>
        <p className="text-xs text-text-tertiary">Written to the audit log with your name.</p>
        {error && (
          <p role="alert" className="text-sm text-red-400">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button variant="ghost" size="sm" onClick={onCancel} disabled={busy}>
            Cancel
          </Button>
          <Button variant={danger ? 'danger' : 'primary'} size="sm" disabled={busy || trimmed.length < MIN_REASON_LENGTH} onClick={() => onConfirm(trimmed)}>
            {confirmLabel}
          </Button>
        </div>
      </div>
    </div>
  );
}
