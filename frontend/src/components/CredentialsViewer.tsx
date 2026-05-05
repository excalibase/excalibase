import { useState } from 'react';
import { useCredentials } from '../hooks/useProvisioning';
import { Button } from './Button';
import { Copy, Eye, EyeOff, Loader2 } from 'lucide-react';

interface CredentialsViewerProps {
  readonly projectId: string;
}

export function CredentialsViewer({ projectId }: CredentialsViewerProps) {
  const { data: credentials, isLoading, error } = useCredentials(projectId);
  const [showPassword, setShowPassword] = useState(false);
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

  return (
    <div className="bg-bg-tertiary rounded-lg p-4 space-y-3">
      <h4 className="text-sm font-medium text-text-primary mb-3">Connection Credentials</h4>

      <div className="space-y-2">
        <div className="flex items-center justify-between">
          <span className="text-sm text-text-tertiary">Host:</span>
          <div className="flex items-center gap-2">
            <code className="text-sm text-text-primary bg-bg-primary px-2 py-1 rounded">
              {credentials.host}
            </code>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => copyToClipboard(credentials.host, 'host')}
            >
              <Copy className="w-4 h-4" />
            </Button>
          </div>
        </div>

        <div className="flex items-center justify-between">
          <span className="text-sm text-text-tertiary">Port:</span>
          <div className="flex items-center gap-2">
            <code className="text-sm text-text-primary bg-bg-primary px-2 py-1 rounded">
              {credentials.port}
            </code>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => copyToClipboard(String(credentials.port), 'port')}
            >
              <Copy className="w-4 h-4" />
            </Button>
          </div>
        </div>

        <div className="flex items-center justify-between">
          <span className="text-sm text-text-tertiary">Database:</span>
          <div className="flex items-center gap-2">
            <code className="text-sm text-text-primary bg-bg-primary px-2 py-1 rounded">
              {credentials.databaseName}
            </code>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => copyToClipboard(credentials.databaseName, 'database')}
            >
              <Copy className="w-4 h-4" />
            </Button>
          </div>
        </div>

        <div className="flex items-center justify-between">
          <span className="text-sm text-text-tertiary">Username:</span>
          <div className="flex items-center gap-2">
            <code className="text-sm text-text-primary bg-bg-primary px-2 py-1 rounded">
              {credentials.username}
            </code>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => copyToClipboard(credentials.username, 'username')}
            >
              <Copy className="w-4 h-4" />
            </Button>
          </div>
        </div>

        <div className="flex items-center justify-between">
          <span className="text-sm text-text-tertiary">Password:</span>
          <div className="flex items-center gap-2">
            <code className="text-sm text-text-primary bg-bg-primary px-2 py-1 rounded">
              {showPassword ? credentials.password : '••••••••'}
            </code>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => setShowPassword(!showPassword)}
            >
              {showPassword ? <EyeOff className="w-4 h-4" /> : <Eye className="w-4 h-4" />}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              onClick={() => copyToClipboard(credentials.password, 'password')}
            >
              <Copy className="w-4 h-4" />
            </Button>
          </div>
        </div>
      </div>

      <div className="pt-3 border-t border-border-primary">
        <div className="flex items-center justify-between">
          <span className="text-sm text-text-tertiary">Connection String:</span>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => copyToClipboard(credentials.connectionString, 'connectionString')}
          >
            <Copy className="w-4 h-4 mr-2" />
            {copied === 'connectionString' ? 'Copied!' : 'Copy'}
          </Button>
        </div>
        <code className="block text-xs text-text-primary bg-bg-primary px-3 py-2 rounded mt-2 break-all">
          {credentials.connectionString}
        </code>
      </div>
    </div>
  );
}
