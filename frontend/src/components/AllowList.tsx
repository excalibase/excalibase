import { useState } from 'react';
import { Loader2, Plus, X } from 'lucide-react';
import { Button } from './Button';
import { serverErrorMessage } from '../utils/serverError';

interface AllowListProps {
  readonly name: string;
  readonly entries: readonly string[];
  readonly emptyText: string;
  readonly inputLabel: string;
  readonly placeholder: string;
  readonly addLabel: string;
  readonly busy: boolean;
  readonly error: unknown;
  readonly onAdd: (entry: string, done: () => void) => void;
  readonly onRemove: (entry: string) => void;
}

// AllowList is a list of entries with a remove button each and a one-field
// add form. The server validates and canonicalises; its refusal is shown.
export function AllowList({
  name, entries, emptyText, inputLabel, placeholder, addLabel, busy, error, onAdd, onRemove,
}: AllowListProps) {
  const [draft, setDraft] = useState('');
  const entry = draft.trim();
  const inputId = `${name}-input`;

  const submit = (event: React.FormEvent) => {
    event.preventDefault();
    if (!entry || busy) return;
    onAdd(entry, () => setDraft(''));
  };

  return (
    <>
      {entries.length === 0 ? (
        <p className="text-xs text-text-tertiary mb-3" data-testid={`${name}-empty`}>{emptyText}</p>
      ) : (
        <ul className="mb-3 divide-y divide-border-primary rounded-md border border-border-primary" data-testid={`${name}-list`}>
          {entries.map((item) => (
            <li key={item} className="flex items-center justify-between px-3 py-2">
              <code className="font-mono text-xs text-text-primary">{item}</code>
              <button
                type="button"
                onClick={() => onRemove(item)}
                disabled={busy}
                aria-label={`Remove ${item}`}
                className="p-1 text-text-tertiary hover:text-red-400 disabled:opacity-50"
              >
                <X className="w-3.5 h-3.5" />
              </button>
            </li>
          ))}
        </ul>
      )}
      <form onSubmit={submit} className="flex items-end gap-2">
        <div className="flex-1">
          <label htmlFor={inputId} className="block text-xs text-text-secondary mb-1">{inputLabel}</label>
          <input
            id={inputId}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            placeholder={placeholder}
            autoComplete="off"
            spellCheck={false}
            className="w-full px-3 py-1.5 bg-bg-tertiary border border-border-primary rounded-lg text-sm font-mono text-text-primary"
          />
        </div>
        <Button type="submit" size="sm" disabled={!entry || busy} className="flex items-center gap-1">
          {busy ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Plus className="w-3.5 h-3.5" />}
          {addLabel}
        </Button>
      </form>
      {error != null && (
        <p role="alert" className="text-xs text-red-400 mt-2 break-words">
          {serverErrorMessage(error, 'The change was not made')}
        </p>
      )}
    </>
  );
}
