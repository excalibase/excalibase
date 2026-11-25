import { useState } from 'react';
import { Check, Copy, Eye, EyeOff } from 'lucide-react';
import type { EnvVar } from '../../api/apps';
import { looksSecret } from '../containers/appFormModel';

const MASK = '••••••••';
const ICON_BUTTON = 'p-1.5 rounded-lg text-text-secondary hover:text-text-primary hover:bg-surface-hover';

// A secret's value is write-only on the server and never reaches the page, so it has nothing to reveal or copy.
function SecretValue() {
  return <span className="text-text-secondary">{MASK} Set</span>;
}

function ReferenceValue({ variable }: { readonly variable: NonNullable<EnvVar['reference']> }) {
  return <span className="text-text-secondary">Linked to database {variable.sourceName} ({variable.variable})</span>;
}

type CopyState = 'idle' | 'copied' | 'failed';

// A plain value. One that is shaped like a credential stays masked until Reveal is clicked; copying also waits for a click.
function PlainValue({ name, value }: { readonly name: string; readonly value: string }) {
  const [revealed, setRevealed] = useState(false);
  const [copy, setCopy] = useState<CopyState>('idle');
  const masked = looksSecret(name) && !revealed;
  const copyValue = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopy('copied');
    } catch {
      setCopy('failed');
    }
  };
  return (
    <span className="flex min-w-0 flex-1 items-center gap-1">
      <code className="min-w-0 flex-1 truncate text-text-primary">{masked ? MASK : value}</code>
      {looksSecret(name) && (
        <button type="button" onClick={() => setRevealed(!revealed)} aria-label={`${revealed ? 'Hide' : 'Reveal'} ${name}`} className={ICON_BUTTON}>
          {revealed ? <EyeOff className="w-4 h-4" aria-hidden="true" /> : <Eye className="w-4 h-4" aria-hidden="true" />}
        </button>
      )}
      {!masked && (
        <button type="button" onClick={copyValue} aria-label={`Copy ${name}`} className={ICON_BUTTON}>
          {copy === 'copied' ? <Check className="w-4 h-4" aria-hidden="true" /> : <Copy className="w-4 h-4" aria-hidden="true" />}
        </button>
      )}
      {copy === 'copied' && <span className="text-xs text-text-secondary">Copied</span>}
      {copy === 'failed' && <span role="alert" className="text-xs text-red-400">The browser refused the clipboard</span>}
    </span>
  );
}

function Value({ variable }: { readonly variable: EnvVar }) {
  if (variable.kind === 'secret') return <SecretValue />;
  if (variable.kind === 'reference' && variable.reference) return <ReferenceValue variable={variable.reference} />;
  // The server drops plain values for a Viewer: names and kinds only.
  if (variable.value === undefined) return <span className="text-text-tertiary">Hidden for your role</span>;
  return <PlainValue name={variable.name} value={variable.value} />;
}

export function VariableRow({ variable }: { readonly variable: EnvVar }) {
  return (
    <li data-testid={`variable-${variable.name}`} className="flex items-center gap-4 px-4 py-2.5 text-sm">
      <span className="w-48 flex-shrink-0 truncate font-mono text-text-primary">{variable.name}</span>
      <Value variable={variable} />
    </li>
  );
}
