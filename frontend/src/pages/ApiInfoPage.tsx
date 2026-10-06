import { useRouteProjectId } from '../hooks/useRouteProjectId';
import { ConnectSnippets } from '../components/ConnectSnippets';

/** The project's public API: the addresses a developer calls and a snippet that runs as copied. */
export function ApiInfoPage() {
  const projectId = useRouteProjectId();
  return (
    <div className="max-w-4xl space-y-4" data-testid="api-info-page">
      <div>
        <h3 className="text-lg font-semibold text-text-primary">API</h3>
        <p className="text-sm text-text-secondary">GraphQL and REST are generated from your database schema.</p>
      </div>
      <div className="rounded-lg border border-border-primary bg-surface-card p-4">
        <ConnectSnippets projectId={projectId} />
      </div>
    </div>
  );
}
