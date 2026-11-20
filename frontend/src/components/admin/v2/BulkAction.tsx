import { useState } from 'react';
import { ReasonDialog } from '../ReasonDialog';
import { runBulk, type BulkResult } from './adminRows';

// One action on several rows. The reason is asked once and written against
// every item, each of which is still sent and audited on its own.
export interface BulkRequest<T> {
  title: string;
  description: string;
  confirmLabel: string;
  danger?: boolean;
  items: T[];
  act: (item: T, reason: string) => Promise<unknown>;
  label: (item: T) => string;
  // "Suspended 3 of 4 accounts" is "<done> of <total> <noun>" after the verb.
  verb: string;
  noun: string;
  onSettled: () => void;
}

export interface BulkOutcome {
  summary: string;
  failures: string[];
}

interface BulkDialogProps<T> {
  readonly request: BulkRequest<T>;
  readonly onFinished: (outcome: BulkOutcome) => void;
  readonly onCancel: () => void;
}

export function BulkDialog<T>({ request, onFinished, onCancel }: BulkDialogProps<T>) {
  const [busy, setBusy] = useState(false);
  const run = async (reason: string) => {
    setBusy(true);
    const result: BulkResult<T> = await runBulk(request.items, (item) => request.act(item, reason));
    setBusy(false);
    request.onSettled();
    onFinished({
      summary: `${request.verb} ${result.done} of ${request.items.length} ${request.noun}.`,
      failures: result.failed.map((failure) => `${request.label(failure.item)}: ${failure.message}`),
    });
  };
  return (
    <ReasonDialog
      title={request.title}
      description={request.description}
      confirmLabel={request.confirmLabel}
      danger={request.danger}
      busy={busy}
      onConfirm={run}
      onCancel={onCancel}
    />
  );
}

export function BulkOutcomeNotice({ outcome, onDismiss }: { readonly outcome: BulkOutcome; readonly onDismiss: () => void }) {
  const failed = outcome.failures.length > 0;
  return (
    <div role="status" className={`rounded-lg border border-border-primary px-3 py-2 text-sm flex items-start justify-between gap-3 ${failed ? 'text-amber-400' : 'text-green-400'}`}>
      <div>
        <p>{outcome.summary}</p>
        {outcome.failures.length > 0 && (
          <ul className="mt-1 list-disc pl-5 text-xs">
            {outcome.failures.map((line) => <li key={line}>{line}</li>)}
          </ul>
        )}
      </div>
      <button type="button" className="text-text-tertiary hover:text-text-primary" onClick={onDismiss}>Dismiss</button>
    </div>
  );
}
