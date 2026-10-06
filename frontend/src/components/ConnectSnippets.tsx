import { useState } from 'react';
import { Link } from 'react-router-dom';
import { Check, Copy } from 'lucide-react';
import { useApiUrl } from '../hooks/useDeploymentMode';
import { useTables } from '../hooks/useSchemaTables';
import { connectEndpoints, curlSnippet, sdkInstall, sdkSnippet } from '../utils/connectSnippets';
import { cn } from '../utils/cn';

interface ConnectSnippetsProps {
  readonly projectId: string;
}

type Tab = 'sdk' | 'curl';

function CopyButton({ value, label }: { readonly value: string; readonly label: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    await navigator.clipboard?.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };
  return (
    <button type="button" onClick={copy} aria-label={label} className="p-1.5 text-text-tertiary hover:text-text-primary">
      {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
    </button>
  );
}

function Endpoint({ label, value }: { readonly label: string; readonly value: string }) {
  return (
    <div>
      <div className="text-xs text-text-tertiary mb-1">{label}</div>
      <div className="flex items-center gap-2 bg-bg-tertiary border border-border-primary rounded px-3 py-1.5">
        <code className="flex-1 text-xs font-mono text-text-primary break-all">{value}</code>
        <CopyButton value={value} label={`Copy ${label}`} />
      </div>
    </div>
  );
}

/** How to reach this project from code: endpoints plus SDK and curl snippets that run as copied. */
export function ConnectSnippets({ projectId }: ConnectSnippetsProps) {
  const { apiUrl, isLoading } = useApiUrl();
  const tables = useTables(projectId);
  const [tab, setTab] = useState<Tab>('sdk');
  if (isLoading) return null;
  if (!apiUrl) {
    return (
      <div className="space-y-3">
        <Endpoint label="Project ID" value={projectId} />
        <p data-testid="connect-unavailable" className="text-sm text-text-secondary">
          This installation does not report its public API address, so no connection snippet can be shown. The
          operator sets it with PUBLIC_BASE_URL.
        </p>
      </div>
    );
  }
  const table = tables.data?.[0]?.name;
  const urls = connectEndpoints(apiUrl, projectId);
  const code = tab === 'sdk' ? sdkSnippet(apiUrl, projectId, table) : curlSnippet(apiUrl, projectId, table);
  return (
    <div className="space-y-3" data-testid="connect-snippets">
      <Endpoint label="Project ID" value={projectId} />
      <Endpoint label="API URL" value={urls.apiUrl} />
      <Endpoint label="GraphQL endpoint" value={urls.graphql} />
      <Endpoint label="REST endpoint" value={urls.rest} />
      <p className="text-xs text-text-secondary">
        Calls need a key: create a publishable key under{' '}
        <Link to={`/project/${projectId}/api-keys`} className="text-purple-400 hover:underline">API Keys</Link>.
        A table is reachable once its API permissions let the caller&apos;s role read it.
      </p>
      <div role="tablist" className="flex gap-1 border-b border-border-primary">
        {(['sdk', 'curl'] as const).map((t) => (
          <button
            key={t}
            type="button"
            role="tab"
            aria-selected={tab === t}
            onClick={() => setTab(t)}
            className={cn('px-3 py-1.5 text-xs', tab === t ? 'text-purple-400 border-b-2 border-purple-400' : 'text-text-tertiary')}
          >
            {t === 'sdk' ? 'JavaScript SDK' : 'curl'}
          </button>
        ))}
      </div>
      {tab === 'sdk' && <Endpoint label="Install" value={sdkInstall} />}
      <div className="relative">
        <pre className="bg-bg-tertiary border border-border-primary rounded px-3 py-2 text-xs font-mono text-text-primary overflow-x-auto">
          <code data-testid="connect-code">{code}</code>
        </pre>
        <div className="absolute top-1 right-1">
          <CopyButton value={code} label="Copy snippet" />
        </div>
      </div>
    </div>
  );
}
