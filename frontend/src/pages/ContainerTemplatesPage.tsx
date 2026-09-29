import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ArrowLeft, Database, Network } from 'lucide-react';
import {
  formatBytes,
  templateRefusal,
  useAppTemplates,
  useDeployTemplate,
  type AppTemplate,
  type TemplateApp,
  type TemplateDeployResult,
  type TemplateVar,
} from '../api/appTemplates';
import { apiErrorMessage } from '../api/apps';
import {
  ContainersHeader,
  HostingGate,
  Spinner,
  ToneBadge,
  primaryButton,
  secondaryButton,
} from '../components/containers/ContainerBits';

const SOURCE_TEXT: Record<TemplateVar['source'], string> = {
  literal: '',
  generated: 'generated on deploy, kept as a secret',
  database: "from this project's database",
  app: 'from another app in this template',
};

function costLine(t: AppTemplate): string {
  const { fit } = t;
  const parts = [
    `Adds ${fit.appsNeeded} ${fit.appsNeeded === 1 ? 'app' : 'apps'} (${fit.appsHeld + fit.appsNeeded} of ${fit.appsAllowed} apps your ${fit.plan} plan allows)`,
    `each copy runs with ${fit.appCpu} CPU and ${fit.appMemory} memory`,
  ];
  if (fit.diskBytes > 0)
    parts.push(`disks ${formatBytes(fit.diskBytes)} in total (your plan allows up to ${formatBytes(fit.diskCapBytes)} per app)`);
  return parts.join(' · ');
}

function networkLine(t: AppTemplate): string | null {
  if (!t.needsPrivateNetwork) return null;
  if (t.fit.privateNetworkOn) return "Uses the project's private network, which is already on.";
  if (t.fit.canTurnOnPrivateNetwork)
    return "Turns on the project's private network: every app in this project can then reach every other app in it.";
  return "Turns on the project's private network, which only an org admin or owner can do.";
}

// A template can go in when the plan has room and, if it opens the network, the caller may open it.
function blocked(t: AppTemplate): boolean {
  if (t.fit.refusals.length > 0) return true;
  return t.needsPrivateNetwork && !t.fit.privateNetworkOn && !t.fit.canTurnOnPrivateNetwork;
}

function AppDetails({ app }: { readonly app: TemplateApp }) {
  const kind = app.internal
    ? `Internal service on port${(app.internalPorts ?? []).length > 1 ? 's' : ''} ${(app.internalPorts ?? []).join(', ')}`
    : `Public web app on port ${app.port}`;
  return (
    <li className="py-2">
      <p className="text-sm text-text-primary">
        <span className="font-medium">{app.name}</span> · {kind}
        {app.disk && ` · ${app.disk.size} disk at ${app.disk.mountPath}`}
      </p>
      <p className="text-xs font-mono text-text-tertiary break-all">{app.image}</p>
      {app.args && app.args.length > 0 && (
        <p className="text-xs font-mono text-text-tertiary">args: {app.args.join(' ')}</p>
      )}
      {app.env.length > 0 && (
        <ul className="mt-1 space-y-0.5">
          {app.env.map((v) => (
            <li key={v.name} className="text-xs text-text-secondary">
              <code className="font-mono">{v.name}</code>{' '}
              {v.source === 'literal' ? <code className="font-mono">= {v.value}</code> : `— ${SOURCE_TEXT[v.source]}`}
            </li>
          ))}
        </ul>
      )}
    </li>
  );
}

function Deployed({ projectId, result }: { readonly projectId: string; readonly result: TemplateDeployResult }) {
  return (
    <div className="mt-3 rounded-md border border-green-500/30 bg-green-500/10 p-3 text-sm" data-testid="template-deployed">
      <p className="text-text-primary">
        Created {result.apps.map((a) => a.name).join(', ')}. Their first deploys are rolling out.
        {result.privateNetworkTurnedOn && " The project's private network is now on."}
      </p>
      <Link to={`/project/${projectId}/containers`} className="text-xs text-purple-400 hover:underline">
        Go to Containers
      </Link>
    </div>
  );
}

function TemplateCard({ projectId, template }: { readonly projectId: string; readonly template: AppTemplate }) {
  const [open, setOpen] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const deploy = useDeployTemplate(projectId);
  const network = networkLine(template);
  const turnsOnNetwork = template.needsPrivateNetwork && !template.fit.privateNetworkOn;

  const confirm = () => {
    setConfirming(false);
    deploy.mutate({ templateId: template.id, confirmPrivateNetwork: turnsOnNetwork });
  };

  return (
    <div className="bg-surface-card border border-border-primary rounded-lg p-4" data-testid={`template-${template.id}`}>
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h4 className="text-sm font-medium text-text-primary">{template.name}</h4>
          <p className="text-sm text-text-secondary">{template.summary}</p>
        </div>
        <div className="flex gap-2 flex-shrink-0">
          <ToneBadge label={`${template.apps.length} ${template.apps.length === 1 ? 'app' : 'apps'}`} tone="neutral" />
        </div>
      </div>
      <p className="text-xs text-text-secondary mt-2" data-testid="template-cost">{costLine(template)}</p>
      {network && (
        <p className="text-xs text-text-secondary mt-1 flex items-center gap-1" data-testid="template-network">
          <Network className="w-3 h-3 flex-shrink-0" /> {network}
        </p>
      )}
      {template.needsDatabase && (
        <p className="text-xs text-text-secondary mt-1 flex items-center gap-1">
          <Database className="w-3 h-3 flex-shrink-0" />
          {template.fit.databaseReady
            ? "Uses this project's database."
            : "Needs this project's database, which is not ready."}
        </p>
      )}
      {template.fit.refusals.length > 0 && (
        <ul role="alert" className="text-xs text-red-400 mt-2 list-disc pl-4">
          {template.fit.refusals.map((reason) => (
            <li key={reason}>{reason}</li>
          ))}
        </ul>
      )}
      {open && (
        <div className="mt-3 border-t border-border-primary pt-2">
          {template.description && <p className="text-xs text-text-secondary mb-2">{template.description}</p>}
          <ul className="divide-y divide-border-primary" data-testid="template-apps">
            {template.apps.map((app) => (
              <AppDetails key={app.name} app={app} />
            ))}
          </ul>
        </div>
      )}
      {confirming && (
        <div className="mt-3 rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-xs text-text-primary">
          <p>
            This creates {template.apps.map((a) => a.name).join(' and ')} and deploys them.
            {turnsOnNetwork &&
              " It also turns on the project's private network: every app in this project will be able to connect to every other app in it."}{' '}
            If any step fails, everything it created is removed again.
          </p>
          <div className="mt-2 flex gap-2">
            <button type="button" className={primaryButton} onClick={confirm} data-testid={`template-confirm-${template.id}`}>
              {turnsOnNetwork ? 'Turn on the network and deploy' : 'Deploy'}
            </button>
            <button type="button" className={secondaryButton} onClick={() => setConfirming(false)}>
              Cancel
            </button>
          </div>
        </div>
      )}
      {deploy.isError && (
        <ul role="alert" className="text-xs text-red-400 mt-2 list-disc pl-4">
          {templateRefusal(deploy.error).map((reason) => (
            <li key={reason}>{reason}</li>
          ))}
        </ul>
      )}
      {deploy.data && <Deployed projectId={projectId} result={deploy.data} />}
      <div className="mt-3 flex gap-2">
        <button
          type="button"
          className={secondaryButton}
          onClick={() => setOpen((v) => !v)}
          data-testid={`template-details-${template.id}`}
        >
          {open ? 'Hide details' : 'Details'}
        </button>
        <button
          type="button"
          className={primaryButton}
          disabled={blocked(template) || deploy.isPending || confirming || !!deploy.data}
          onClick={() => setConfirming(true)}
          data-testid={`template-deploy-${template.id}`}
        >
          {deploy.isPending ? 'Deploying…' : 'Deploy'}
        </button>
      </div>
    </div>
  );
}

function TemplateList({ projectId }: { readonly projectId: string }) {
  const { data: templates, error } = useAppTemplates(projectId);
  if (error) {
    return (
      <div role="alert" className="text-sm text-red-400 py-6">
        {apiErrorMessage(error, 'Could not load the templates')}
      </div>
    );
  }
  if (!templates) return <Spinner />;
  return (
    <div className="space-y-3" data-testid="templates-list">
      {templates.map((t) => (
        <TemplateCard key={t.id} projectId={projectId} template={t} />
      ))}
    </div>
  );
}

export function ContainerTemplatesPage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  return (
    <div data-testid="container-templates-page">
      <ContainersHeader
        title="Templates"
        subtitle="Deploy a set of apps, disks and variables in one step. All or nothing: a failure removes what it created."
        actions={
          <Link to={`/project/${projectId}/containers`} className={secondaryButton}>
            <ArrowLeft className="w-4 h-4" /> Containers
          </Link>
        }
      />
      <HostingGate>
        <TemplateList projectId={projectId} />
      </HostingGate>
    </div>
  );
}
