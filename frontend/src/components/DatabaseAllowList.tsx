import { useEffect, useState } from 'react';
import { ShieldCheck, X } from 'lucide-react';
import { hostRange, useAllowedSources, useSetAllowedSources, type AllowedSources } from '../api/allowedSources';
import { refusalMessage } from '../api/clusterSettings';
import { Button } from './Button';

interface DatabaseAllowListProps {
  readonly projectId: string;
}

const inputClass = 'w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary text-sm';

function stateText(current: AllowedSources): string {
  switch (current.access) {
    case 'host':
      return current.singleHost
        ? 'This host only: the database listens on 127.0.0.1, so a client elsewhere connects over an SSH tunnel.'
        : 'This host only.';
    case 'any':
      return current.singleHost
        ? 'Any address: this host publishes its databases on every interface.'
        : 'Any address on the internet may reach the port.';
    case 'listed':
      return 'Only the addresses below may reach the port.';
    default:
      return current.publicEnabled
        ? 'Any address: this port was opened before allow-lists. Choose who may connect below.'
        : 'Not chosen yet. Choose who may connect before opening the port.';
  }
}

// DatabaseAllowList shows and edits who may reach the public port (EXC-665).
// The server validates every entry; Studio only collects them.
export function DatabaseAllowList({ projectId }: DatabaseAllowListProps) {
  const query = useAllowedSources(projectId, true);
  const save = useSetAllowedSources(projectId);
  const current = query.data;
  const [sources, setSources] = useState<string[]>([]);
  const [allowAny, setAllowAny] = useState(false);
  const [draft, setDraft] = useState('');

  useEffect(() => {
    if (!current) return;
    setSources(current.sources);
    setAllowAny(current.access === 'any');
  }, [current]);

  if (query.isError) {
    return (
      <p className="text-xs text-text-tertiary mt-3" data-testid="allow-list">
        Who may connect could not be read: {refusalMessage(query.error)}
      </p>
    );
  }
  if (!current) return null;
  const editable = current.canChange && !current.singleHost;

  const add = (entry: string) => {
    const value = entry.trim();
    if (value === '' || sources.includes(value)) return;
    setSources([...sources, value]);
    setAllowAny(false);
  };

  return (
    <div className="mt-4 border-t border-border-primary pt-3" data-testid="allow-list">
      <h5 className="flex items-center gap-2 text-sm font-medium text-text-primary mb-1">
        <ShieldCheck className="w-4 h-4" /> Who may connect
      </h5>
      <p className="text-xs text-text-secondary mb-2" data-testid="allow-list-state">
        {stateText(current)}
      </p>
      {sources.length > 0 && !allowAny && (
        <ul className="flex flex-wrap gap-2 mb-2" data-testid="allow-list-sources">
          {sources.map((source) => (
            <li key={source} className="flex items-center gap-1 rounded-md border border-border-primary px-2 py-0.5 text-xs font-mono">
              {source}
              {editable && (
                <button
                  type="button"
                  aria-label={`Remove ${source}`}
                  className="text-text-tertiary hover:text-text-primary"
                  onClick={() => setSources(sources.filter((other) => other !== source))}
                >
                  <X className="w-3 h-3" />
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {editable && (
        <AllowListEditor
          current={current}
          allowAny={allowAny}
          draft={draft}
          setDraft={setDraft}
          setAllowAny={setAllowAny}
          add={(entry) => {
            add(entry);
            setDraft('');
          }}
          saving={save.isPending}
          onSave={() => save.mutate({ allowAny, sources })}
        />
      )}
      {save.isError && (
        <p role="alert" className="text-xs text-color-error mt-2">
          {refusalMessage(save.error)}
        </p>
      )}
    </div>
  );
}

interface EditorProps {
  readonly current: AllowedSources;
  readonly allowAny: boolean;
  readonly draft: string;
  readonly saving: boolean;
  readonly setDraft: (value: string) => void;
  readonly setAllowAny: (value: boolean) => void;
  readonly add: (entry: string) => void;
  readonly onSave: () => void;
}

function AllowListEditor({ current, allowAny, draft, saving, setDraft, setAllowAny, add, onSave }: EditorProps) {
  return (
    <div className="space-y-2">
      {!allowAny && (
        <div className="flex gap-2">
          <label htmlFor="allow-list-entry" className="sr-only">
            Address or range
          </label>
          <input
            id="allow-list-entry"
            className={inputClass}
            value={draft}
            placeholder="203.0.113.7 or 198.51.100.0/24"
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault();
                add(draft);
              }
            }}
          />
          <Button size="sm" variant="secondary" type="button" onClick={() => add(draft)}>
            Add
          </Button>
        </div>
      )}
      {!allowAny && current.callerAddress && (
        <Button size="sm" variant="ghost" type="button" onClick={() => add(hostRange(current.callerAddress))}>
          Add my address ({current.callerAddress})
        </Button>
      )}
      <label htmlFor="allow-list-any" className="flex items-center gap-2 text-xs text-text-secondary">
        <input id="allow-list-any" type="checkbox" checked={allowAny} onChange={(e) => setAllowAny(e.target.checked)} />
        <span>Allow any address</span>
      </label>
      {allowAny && (
        <p className="rounded-md border border-amber-500/30 bg-amber-500/10 p-2 text-xs text-text-primary" data-testid="allow-list-any-warning">
          Anyone on the internet will be able to reach this database&apos;s login. Prefer listing the addresses that need it.
        </p>
      )}
      <p className="text-xs text-text-tertiary">
        Up to {current.max} addresses or ranges, at least one IPv4. A shared address (office NAT, VPN, mobile carrier) admits
        everyone behind it.
      </p>
      <Button size="sm" type="button" disabled={saving} onClick={onSave} data-testid="allow-list-save">
        Save
      </Button>
    </div>
  );
}
