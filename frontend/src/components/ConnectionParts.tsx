import { useState } from 'react';
import { Copy, Check } from 'lucide-react';
import type { ProjectEndpoint } from '../api/projectEndpoint';
import type { CredentialsResponse } from '../types';

// What every engine's connection section shares: where a client dials and
// the row that shows a string and copies it.

// A target is one address a client can dial. It exists only when the customer
// opened a public port and it is answering.
export interface Target {
  host: string;
  port: number;
}

// publicTarget is the port a client outside the cluster dials, or null when
// the project publishes none or it is not answering yet.
export function publicTarget(endpoint?: ProjectEndpoint): Target | null {
  if (!endpoint?.publicEnabled || !endpoint.available || endpoint.port <= 0) return null;
  return { host: endpoint.host, port: endpoint.port };
}

// Where a string works.
export function publicLabel(endpoint?: ProjectEndpoint): string {
  return endpoint?.singleHost ? 'On the host — 127.0.0.1' : 'Public — from outside the cluster';
}

export const PASSWORD_PLACEHOLDER = '<password>';

// The address a customer's own machine should be told about: the public one
// when it answers, otherwise none, because the database cannot be reached
// from the internet.
export type ConnectAddress =
  | { kind: 'public'; host: string; port: number }
  | { kind: 'none' };

export function connectAddress(endpoint: ProjectEndpoint | undefined): ConnectAddress {
  const external = publicTarget(endpoint);
  return external ? { kind: 'public', ...external } : { kind: 'none' };
}

// userinfo is a string's user:password with the placeholder where the secret
// goes; a stored password is never shown.
export function userinfo(username: string): string {
  return `${encodeURIComponent(username)}:${PASSWORD_PLACEHOLDER}`;
}

export const NOT_REACHABLE =
  'Not reachable from the internet. Turn on public access (with an IP allowlist) to connect from your own machine.';

export interface SectionProps {
  readonly projectId: string;
  readonly credentials: CredentialsResponse;
  readonly endpoint?: ProjectEndpoint;
  readonly hostsApps: boolean;
}

interface StringRowProps {
  readonly testId: string;
  readonly label: string;
  readonly value?: string;
  // Other cards mask a secret on screen and copy the real one; connection
  // strings carry no secret, so these default to the value.
  readonly shown?: string;
  readonly copied?: string;
}

// StringRow shows a connection string with the password placeholder and
// copies the same text.
export function StringRow({ testId, label, value = '', shown = value, copied = value }: StringRowProps) {
  const [justCopied, setJustCopied] = useState(false);
  const onCopy = () => {
    navigator.clipboard.writeText(copied).then(
      () => {
        setJustCopied(true);
        setTimeout(() => setJustCopied(false), 1500);
      },
      () => setJustCopied(false),
    );
  };
  return (
    <div>
      <div className="text-xs text-text-tertiary mb-1" data-testid={`${testId}-label`}>{label}</div>
      <div className="flex items-center gap-2">
        <code
          data-testid={testId}
          className="flex-1 bg-bg-tertiary border border-border-primary rounded px-3 py-2 text-xs font-mono text-text-primary break-all"
        >
          {shown}
        </code>
        <button
          type="button"
          onClick={onCopy}
          aria-label={`Copy ${label}`}
          data-testid={`conn-copy-${testId.replace('conn-', '')}`}
          className="p-2 rounded border border-border-primary text-text-secondary hover:text-text-primary hover:border-text-secondary transition-colors"
        >
          {justCopied ? <Check className="w-4 h-4 text-green-400" /> : <Copy className="w-4 h-4" />}
        </button>
      </div>
    </div>
  );
}

