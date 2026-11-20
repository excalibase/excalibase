import { useState } from 'react';
import { useCredentials } from '../hooks/useProvisioning';
import { useHostsApps } from '../hooks/useHostsApps';
import { AppsConnectionNote } from './AppsConnectionNote';
import { useProjectEndpoint } from '../api/projectEndpoint';
import { Button } from './Button';
import { Copy, Loader2 } from 'lucide-react';
import { NOT_REACHABLE, connectAddress, userinfo } from './ConnectionParts';
import { ResetPasswordButton } from './ResetPasswordButton';

interface CredentialsViewerProps {
  readonly projectId: string;
}

export function CredentialsViewer({ projectId }: CredentialsViewerProps) {
  const { data: credentials, isLoading, error } = useCredentials(projectId);
  const { data: endpoint } = useProjectEndpoint(projectId);
  const hostsApps = useHostsApps(projectId);
  const [copied, setCopied] = useState<string | null>(null);

  const copyToClipboard = (text: string, field: string) => {
    navigator.clipboard.writeText(text);
    setCopied(field);
    setTimeout(() => setCopied(null), 2000);
  };

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-8">
        <Loader2 className="w-6 h-6 animate-spin text-accent-primary" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="bg-red-900/20 border border-color-error rounded-lg p-4">
        <p className="text-color-error">Failed to load credentials</p>
      </div>
    );
  }

  if (!credentials) return null;

  const address = connectAddress(endpoint);
  const connectionString = address.kind === 'none'
    ? null
    : `postgresql://${userinfo(credentials.username)}@${address.host}:${address.port}/${credentials.databaseName}?sslmode=${credentials.sslMode ?? 'require'}`;
  const rows: [string, string][] = [
    ...(address.kind === 'none' ? [] : [['Host', address.host], ['Port', String(address.port)]] as [string, string][]),
    ['Database', credentials.databaseName],
    ['Username', credentials.username],
  ];

  return (
    <div className="bg-bg-tertiary rounded-lg p-4 space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h4 className="text-sm font-medium text-text-primary">Connection Credentials</h4>
        <ResetPasswordButton projectId={projectId} />
      </div>

      {address.kind === 'none' && !hostsApps && <p className="text-sm text-text-secondary">{NOT_REACHABLE}</p>}
      {hostsApps && <AppsConnectionNote />}

      <div className="space-y-2">
        {rows.map(([label, value]) => (
          <div key={label} className="flex items-center justify-between">
            <span className="text-sm text-text-tertiary">{label}:</span>
            <div className="flex items-center gap-2">
              <code className="text-sm text-text-primary bg-bg-primary px-2 py-1 rounded">{value}</code>
              <Button variant="secondary" size="sm" aria-label={`Copy ${label.toLowerCase()}`} onClick={() => copyToClipboard(value, label)}>
                <Copy className="w-4 h-4" />
              </Button>
            </div>
          </div>
        ))}
        <p className="text-xs text-text-tertiary">
          The password is shown once, when you create the database or reset it. Reset password to get new credentials.
        </p>
      </div>

      {connectionString && (
        <div className="pt-3 border-t border-border-primary">
          <div className="flex items-center justify-between">
            <span className="text-sm text-text-tertiary">Connection String:</span>
            <Button variant="secondary" size="sm" onClick={() => copyToClipboard(connectionString, 'connectionString')}>
              <Copy className="w-4 h-4 mr-2" />
              {copied === 'connectionString' ? 'Copied!' : 'Copy'}
            </Button>
          </div>
          <code className="block text-xs text-text-primary bg-bg-primary px-3 py-2 rounded mt-2 break-all">{connectionString}</code>
        </div>
      )}
    </div>
  );
}
