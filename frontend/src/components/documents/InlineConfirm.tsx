import { useState } from 'react';
import { Trash2 } from 'lucide-react';

interface InlineConfirmProps {
  readonly label: string;
  readonly question: string;
  readonly busy?: boolean;
  readonly onConfirm: () => void;
}

// A destructive action asked about in place, next to the thing it destroys.
export function InlineConfirm({ label, question, busy = false, onConfirm }: InlineConfirmProps) {
  const [asking, setAsking] = useState(false);

  if (!asking) {
    return (
      <button
        type="button"
        aria-label={label}
        onClick={() => setAsking(true)}
        className="p-1 rounded text-text-tertiary hover:text-red-400 hover:bg-red-500/10"
      >
        <Trash2 className="w-3.5 h-3.5" />
      </button>
    );
  }
  return (
    <span className="inline-flex items-center gap-2 text-xs">
      <span className="text-red-400">{question}</span>
      <button
        type="button"
        disabled={busy}
        onClick={() => {
          onConfirm();
          setAsking(false);
        }}
        className="px-2 py-0.5 rounded bg-red-600 text-white hover:bg-red-500 disabled:opacity-50"
      >
        Delete
      </button>
      <button type="button" onClick={() => setAsking(false)} className="px-2 py-0.5 rounded text-text-secondary hover:bg-surface-hover">
        Keep
      </button>
    </span>
  );
}
