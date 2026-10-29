import { Link, useParams } from 'react-router-dom';
import { useAppHostingEnabled, useFeatureEnabled, useSitesEnabled } from '../hooks/useDeploymentMode';
import { useInstance, useInstances } from '../hooks/useProvisioning';
import { DatabaseServiceCard } from '../components/services/DatabaseServiceCard';
import { ContainersServiceCard } from '../components/services/ContainersServiceCard';

// CopiedFrom names the database a copy was made from (EXC-667), linked when
// the caller can see it; a source that is gone is named by its id.
function CopiedFrom({ sourceId, backupId }: { readonly sourceId: string; readonly backupId?: string }) {
  const { data: visible } = useInstances();
  const source = visible?.find((p) => p.projectId === sourceId);
  return (
    <p className="text-xs text-text-tertiary mt-0.5" data-testid="copied-from">
      Copy of{' '}
      {source ? (
        <Link to={`/project/${sourceId}`} className="text-purple-400 hover:underline">
          {source.projectName || sourceId}
        </Link>
      ) : (
        <span className="font-mono">{sourceId}</span>
      )}
      {backupId && <span>, from backup <span className="font-mono">{backupId}</span></span>}
    </p>
  );
}

// A project is a container of services (ADR 0035): the overview lists each
// service as a peer with its own status. Containers appear only when the
// server has app hosting on, so a database-only install never sees them.
export function ProjectOverviewPage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const { enabled: appHosting } = useAppHostingEnabled();
  const sites = useSitesEnabled();
  const { data: project } = useInstance(projectId);
  const { enabled: copyOn } = useFeatureEnabled('dbcopy');
  const projectName = project?.projectName || projectId;

  return (
    <div className="max-w-6xl mx-auto space-y-6" data-testid="project-overview">
      <div>
        <h2 className="text-xl font-bold text-text-primary">{projectName}</h2>
        {projectName !== projectId && (
          <p className="text-xs font-mono text-text-tertiary" data-testid="project-overview-id">{projectId}</p>
        )}
        {copyOn && project?.restoredFromProjectId && (
          <CopiedFrom sourceId={project.restoredFromProjectId} backupId={project.restoredFromBackupId} />
        )}
        <p className="text-sm text-text-tertiary mt-0.5">
          {appHosting
            ? 'The services in this project. Each one runs and reports on its own.'
            : 'The services in this project.'}
        </p>
      </div>
      <div className="grid gap-4 md:grid-cols-2">
        <DatabaseServiceCard projectId={projectId} />
        {appHosting && <ContainersServiceCard projectId={projectId} />}
      </div>
      {!sites.isLoading && !sites.enabled && (
        <p className="text-xs text-text-tertiary" data-testid="sites-unavailable">
          Static frontend hosting isn&apos;t available on this install; deploy your frontend as a{' '}
          <Link to={`/project/${projectId}/containers`} className="text-purple-400 hover:underline">
            container
          </Link>
          .
        </p>
      )}
    </div>
  );
}
