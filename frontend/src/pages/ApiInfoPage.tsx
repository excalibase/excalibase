import { useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Loader2, Copy, Check, Globe, Database, Lock, Terminal, ExternalLink, BookOpen } from 'lucide-react';
import { useState } from 'react';
import { api } from '../api/client';
import type { DatabaseInstance } from '../types';

// sanitizeHost restricts a host value coming from the provisioning API to a
// safe hostname/IP shape before it is interpolated into displayed/copyable
// URLs. This prevents an Open Redirect / URL-spoofing vector where a
// compromised or malformed API response could inject path, query, credential
// or scheme characters into the rendered endpoint URLs.
function sanitizeHost(value: string): string {
  // Allow only DNS hostnames and IPv4/IPv6 literals: letters, digits,
  // dot, hyphen, colon and brackets. Anything else collapses to a safe default.
  return /^[A-Za-z0-9.\-:[\]]+$/.test(value) ? value : 'localhost';
}

// sanitizePort restricts a port to digits only (1-65535 is enforced by the
// backend); a non-numeric value falls back to the supplied default.
function sanitizePort(value: number | string, fallback: string): string {
  const port = String(value);
  return /^[0-9]{1,5}$/.test(port) ? port : fallback;
}

// sanitizeDbName restricts a database identifier to characters valid for a
// PostgreSQL identifier so it cannot break out of the connection string.
function sanitizeDbName(value: string): string {
  return /^[A-Za-z0-9_]+$/.test(value) ? value : 'app';
}

// buildDeploySnippet assembles the curl deploy example. Inlined as a separate
// helper because the embedded JSON requires escaped double quotes which
// confuse static analysis when written as a single template literal (S6535).
function buildDeploySnippet(host: string, port: number | string): string {
  const body = JSON.stringify({
    id: 'hello',
    code: 'function handler(d) { return {msg: "hi"}; }',
  });
  return [
    `curl -X POST http://${host}:${port}/deploy \\`,
    `  -H 'Content-Type: application/json' \\`,
    `  -d '${body}'`,
  ].join('\n');
}

function CopyButton({ text }: { readonly text: string }) {
  const [copied, setCopied] = useState(false);
  const handleCopy = () => {
    navigator.clipboard.writeText(text);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };
  return (
    <button onClick={handleCopy} className="p-1.5 rounded text-text-tertiary hover:text-text-primary hover:bg-surface-hover transition-colors">
      {copied ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
    </button>
  );
}

function EndpointCard({ icon: Icon, title, url, description, snippets }: {
  readonly icon: React.ComponentType<{ className?: string }>; readonly title: string; readonly url: string; readonly description: string;
  readonly snippets?: ReadonlyArray<{ readonly label: string; readonly code: string }>;
}) {
  const [showSnippets, setShowSnippets] = useState(false);
  return (
    <div className="rounded-lg border border-border-primary bg-surface-card p-4">
      <div className="flex items-center gap-2 mb-2">
        <Icon className="w-4 h-4 text-purple-400" />
        <h4 className="text-sm font-medium text-text-primary">{title}</h4>
      </div>
      <p className="text-xs text-text-tertiary mb-3">{description}</p>
      <div className="flex items-center gap-2 bg-bg-primary rounded-lg px-3 py-2">
        <code className="flex-1 text-xs text-text-secondary font-mono truncate">{url}</code>
        <CopyButton text={url} />
      </div>
      {snippets && snippets.length > 0 && (
        <div className="mt-3">
          <button onClick={() => setShowSnippets(!showSnippets)}
            className="text-xs text-purple-400 hover:text-purple-300">
            {showSnippets ? 'Hide' : 'Show'} code examples
          </button>
          {showSnippets && (
            <div className="mt-2 space-y-2">
              {snippets.map((s) => (
                <div key={s.label}>
                  <div className="flex items-center justify-between mb-1">
                    <span className="text-[10px] text-text-tertiary uppercase">{s.label}</span>
                    <CopyButton text={s.code} />
                  </div>
                  <pre className="p-2 rounded bg-bg-primary text-xs text-text-secondary font-mono overflow-x-auto">{s.code}</pre>
                </div>
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

export function ApiInfoPage() {
  const { projectId } = useParams<{ projectId: string }>();

  const { data: project, isLoading } = useQuery({
    queryKey: ['project', projectId],
    queryFn: async () => {
      const res = await api.get<DatabaseInstance>(`/provision/${projectId}`);
      return res.data;
    },
    enabled: !!projectId,
  });

  if (isLoading || !project) {
    return <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>;
  }

  const host = sanitizeHost(project.host || 'localhost');
  const dbPort = sanitizePort(project.port || 5432, '5432');
  const dbName = sanitizeDbName(project.databaseName || 'app');
  const restPort = sanitizePort(import.meta.env.VITE_REST_PORT || '3000', '3000');
  const graphqlPort = sanitizePort(import.meta.env.VITE_GRAPHQL_PORT || '4000', '4000');
  const authPort = sanitizePort(import.meta.env.VITE_AUTH_PORT || '24000', '24000');
  const edgeFnPort = sanitizePort(import.meta.env.VITE_EDGEFN_PORT || '8000', '8000');

  return (
    <div data-testid="api-info-page">
      <h3 className="text-lg font-semibold text-text-primary mb-1">API</h3>
      <p className="text-sm text-text-secondary mb-6">Connection details and API endpoints for {projectId}</p>

      {/* API Documentation section */}
      <div data-testid="api-docs-page" className="mb-6 rounded-lg border border-border-primary bg-surface-card p-4">
        <div className="flex items-center gap-2 mb-2">
          <BookOpen className="w-4 h-4 text-purple-400" />
          <h4 className="text-sm font-medium text-text-primary">API Documentation</h4>
        </div>
        <p className="text-xs text-text-tertiary mb-3">
          Full REST API documentation for excalibase-rest, including query parameters, filtering, and relationship handling.
        </p>
        <a
          href="https://docs.excalibase.dev/rest-api"
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-2 px-3 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg transition-colors"
          data-testid="api-docs-link"
        >
          <ExternalLink className="w-4 h-4" /> Open in new tab
        </a>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <EndpointCard
          icon={Globe}
          title="REST API"
          url={`http://${host}:${restPort}`}
          description="Auto-generated REST API from your database schema. Supports filtering, pagination, and relationships."
          snippets={[
            { label: 'curl', code: `curl http://${host}:${restPort}/users?select=id,email&limit=10` },
            { label: 'javascript', code: `const res = await fetch('http://${host}:${restPort}/users');\nconst users = await res.json();` },
          ]}
        />

        <EndpointCard
          icon={Globe}
          title="GraphQL API"
          url={`http://${host}:${graphqlPort}/graphql`}
          description="Auto-generated GraphQL API with queries, mutations, and subscriptions."
          snippets={[
            { label: 'graphql', code: `query {\n  users(limit: 10) {\n    id\n    email\n  }\n}` },
          ]}
        />

        <EndpointCard
          icon={Lock}
          title="Auth API"
          url={`http://${host}:${authPort}/auth/${projectId}`}
          description="User authentication with JWT tokens, refresh tokens, and role-based access."
          snippets={[
            { label: 'register', code: `curl -X POST http://${host}:${authPort}/auth/${projectId}/register \\\n  -H 'Content-Type: application/json' \\\n  -d '{"email":"user@example.com","password":"secret","fullName":"User"}'` },
          ]}
        />

        <EndpointCard
          icon={Database}
          title="Database Direct"
          url={`postgresql://excalibase_app@${host}:${dbPort}/${dbName}`}
          description="Direct PostgreSQL connection for tools like psql, pgAdmin, or DBeaver."
          snippets={[
            { label: 'psql', code: `psql -h ${host} -p ${dbPort} -U excalibase_app -d ${dbName}` },
          ]}
        />

        <EndpointCard
          icon={Terminal}
          title="Edge Functions"
          url={`http://${host}:${edgeFnPort}`}
          description="Deploy and invoke serverless Deno functions."
          snippets={[
            { label: 'deploy', code: buildDeploySnippet(host, edgeFnPort) },
          ]}
        />

        <EndpointCard
          icon={Globe}
          title="Realtime (CDC)"
          url={`ws://${host}:${restPort}/ws`}
          description="Subscribe to real-time database changes via WebSocket. Powered by excalibase-watcher."
        />
      </div>
    </div>
  );
}
