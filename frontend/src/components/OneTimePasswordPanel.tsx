import { useState } from 'react';
import { Check, Copy, Download } from 'lucide-react';
import { credentialsCsv, credentialsCsvFileName } from '../utils/databaseCredentialsCsv';
import type { ConnectAddress } from './ConnectionParts';

interface OneTimePasswordPanelProps {
  readonly projectId: string;
  readonly password: string;
  readonly address: ConnectAddress;
  readonly database: string;
  readonly user: string;
  readonly sslmode: string;
  // True while the database is still being built: its login name and
  // database are not known yet, and will fill in.
  readonly pending?: boolean;
  readonly onDone: () => void;
}

interface Shown {
  readonly host: string;
  readonly port: string;
}

function shownAddress(address: ConnectAddress): Shown {
  if (address.kind === 'none') return { host: '', port: '' };
  return { host: address.host, port: String(address.port) };
}

const NOT_READY = 'Shown when the database is ready';

function Field({ testId, label, value }: { readonly testId: string; readonly label: string; readonly value: string }) {
  return (
    <div>
      <div className="text-xs text-text-tertiary mb-1">{label}</div>
      <code data-testid={testId} className="block bg-bg-tertiary border border-border-primary rounded px-3 py-2 text-xs font-mono text-text-primary break-all">
        {value || '—'}
      </code>
    </div>
  );
}

// Holds the password in this component's props only, so dismissing it drops
// the last copy the page has. Nothing here writes it to storage or the URL.
export function OneTimePasswordPanel({ projectId, password, address, database, user, sslmode, pending = false, onDone }: OneTimePasswordPanelProps) {
  const [copied, setCopied] = useState(false);
  const shown = shownAddress(address);
  const csv = credentialsCsv({ host: shown.host, port: shown.port, database, user, password, sslmode });
  const href = `data:text/csv;charset=utf-8,${encodeURIComponent(csv)}`;

  const copyPassword = () => {
    navigator.clipboard.writeText(password).then(() => setCopied(true), () => setCopied(false));
  };

  return (
    <section className="rounded-xl border border-amber-500/40 bg-amber-500/10 p-5 space-y-4" data-testid="one-time-password-panel" aria-label="Database password">
      <div>
        <h3 className="text-sm font-semibold text-text-primary">Your database password</h3>
        <p className="text-sm text-text-secondary mt-1">Copy it now. It will not be shown again.</p>
      </div>
      <div className="grid grid-cols-2 gap-3">
        <Field testId="otp-host" label="Host" value={address.kind === 'none' ? 'Not reachable from the internet' : shown.host} />
        <Field testId="otp-port" label="Port" value={shown.port} />
        <Field testId="otp-database" label="Database" value={database || (pending ? NOT_READY : '')} />
        <Field testId="otp-user" label="User" value={user || (pending ? NOT_READY : '')} />
        <Field testId="otp-sslmode" label="SSL mode" value={sslmode} />
      </div>
      {address.kind === 'none' && (
        <p className="text-xs text-text-tertiary" data-testid="otp-address-hint">
          The host and port appear under Connection Credentials on the project&apos;s page once public access is turned on.
        </p>
      )}
      <div className="flex items-center gap-2">
        <code data-testid="otp-password" className="flex-1 bg-bg-tertiary border border-border-primary rounded px-3 py-2 text-sm font-mono text-text-primary break-all">
          {password}
        </code>
        <button
          type="button" onClick={copyPassword} aria-label="Copy password"
          className="p-2 rounded border border-border-primary text-text-secondary hover:text-text-primary"
        >
          {copied ? <Check className="w-4 h-4 text-green-400" /> : <Copy className="w-4 h-4" />}
        </button>
      </div>
      <div className="flex flex-wrap gap-3">
        <a
          href={href} download={credentialsCsvFileName(projectId)}
          className="inline-flex items-center gap-2 px-3 py-2 rounded border border-border-primary text-sm text-text-secondary hover:text-text-primary"
        >
          <Download className="w-4 h-4" /> Download CSV
        </a>
        <button
          type="button" onClick={onDone}
          className="px-4 py-2 rounded bg-accent-primary text-white text-sm font-medium"
        >
          I&apos;ve saved it
        </button>
      </div>
    </section>
  );
}
