import { useState } from 'react';
import { Copy, Check } from 'lucide-react';
import type { ProjectEndpoint } from '../api/projectEndpoint';
import type { CredentialsResponse } from '../types';

// What every engine's connection section shares: where a client dials and
// the row that shows a string masked and copies the real one.

// A target is one address a client can dial. Every project has an internal
// one; a public one exists only when the customer opened a port and it is
// answering.
export interface Target {
  host: string;
  port: number;
}

// internalTarget is where a workload inside the cluster connects. It is shown
// whether or not the project publishes publicly: an app hosted beside the
// database should use it and needs no public port.
export function internalTarget(credentials: CredentialsResponse, endpoint?: ProjectEndpoint): Target {
  if (endpoint) return { host: endpoint.internal.host, port: endpoint.internal.port };
  return { host: credentials.host, port: credentials.port };
}

// publicTarget is the port a client outside the cluster dials, or null when
// the project publishes none or it is not answering yet.
export function publicTarget(endpoint?: ProjectEndpoint): Target | null {
  if (!endpoint?.publicEnabled || !endpoint.available || endpoint.port <= 0) return null;
  return { host: endpoint.host, port: endpoint.port };
}

// Labels for where each string works. A single host's "public" port is on
// the host itself, and its internal address is the container's name.
export function labels(endpoint?: ProjectEndpoint): { internal: string; external: string } {
  if (endpoint?.singleHost) {
    return { internal: 'Internal — from containers on this host', external: 'On the host — 127.0.0.1' };
  }
  return { internal: 'Internal — from inside the cluster', external: 'Public — from outside the cluster' };
}

export interface SectionProps {
  readonly projectId: string;
  readonly credentials: CredentialsResponse;
  readonly endpoint?: ProjectEndpoint;
  readonly shownPassword: string;
}

interface StringRowProps {
  readonly testId: string;
  readonly label: string;
  readonly shown: string;
  readonly copied: string;
}

// StringRow shows a connection string with the password masked and copies the
// real one. What goes to the clipboard is what a client needs; what goes on
// screen is what is safe over someone's shoulder.
export function StringRow({ testId, label, shown, copied }: StringRowProps) {
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

