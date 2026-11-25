import { Link, useParams } from 'react-router-dom';
import { Loader2 } from 'lucide-react';
import { apiErrorMessage, useApps, type App } from '../api/apps';
import { VariableRow } from '../components/variables/VariableRow';
import { panelSearch } from '../components/services/v2/panelState';
import { useAppHostingEnabled } from '../hooks/useDeploymentMode';
import { useDocumentTitle } from '../hooks/useDocumentTitle';
import { useProjectRole } from '../hooks/useProjectRole';

function editLink(projectId: string, app: App): string {
  return `/project/${projectId}?${panelSearch(new URLSearchParams(), { kind: 'app', id: app.id }, 'variables').toString()}`;
}

function ServiceVariables({ projectId, app, canEdit }: { readonly projectId: string; readonly app: App; readonly canEdit: boolean }) {
  return (
    <section aria-label={app.name} className="space-y-2">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-[15px] font-semibold text-text-primary">{app.name}</h2>
        {canEdit && (
          <Link to={editLink(projectId, app)} aria-label={`Edit variables of ${app.name}`} className="text-sm text-purple-400 hover:underline">
            Edit variables
          </Link>
        )}
      </div>
      <ul className="divide-y divide-border-primary rounded-lg border border-border-primary bg-surface-card">
        {app.env.map((variable) => <VariableRow key={variable.name} variable={variable} />)}
      </ul>
    </section>
  );
}

// The variables of every service in the project, in one place (studio_v2). Reading only: a value is
// changed in its service's own Variables tab, which is where the server takes the write.
export function ProjectVariablesPage() {
  useDocumentTitle('Variables');
  const { projectId = '' } = useParams<{ projectId: string }>();
  const { enabled: appHosting, isLoading: configLoading } = useAppHostingEnabled();
  const apps = useApps(projectId, appHosting);
  const { data, error } = apps;
  const isLoading = configLoading || apps.isLoading;
  const { canDevelop } = useProjectRole(projectId);
  const withVariables = (data ?? []).filter((app) => app.env.length > 0);
  return (
    <div className="mx-auto max-w-4xl space-y-4" data-testid="project-variables">
      <div>
        <h1 className="text-xl font-semibold text-text-primary">Variables</h1>
        <p className="text-sm text-text-tertiary">What each service in this project runs with. Secrets stay hidden; plain values show to Developers and above.</p>
      </div>
      {isLoading && <Loader2 className="w-5 h-5 animate-spin text-text-tertiary" aria-label="Loading" />}
      {error && <p role="alert" className="text-sm text-red-400">{apiErrorMessage(error, 'Could not load the variables')}</p>}
      {!isLoading && !appHosting && (
        <p className="text-sm text-text-secondary" data-testid="variables-not-offered">App hosting is not offered on this install, so there are no services with variables.</p>
      )}
      {!isLoading && appHosting && !error && withVariables.length === 0 && (
        <p className="text-sm text-text-secondary">No services with variables yet.</p>
      )}
      {withVariables.map((app) => <ServiceVariables key={app.id} projectId={projectId} app={app} canEdit={canDevelop} />)}
    </div>
  );
}
