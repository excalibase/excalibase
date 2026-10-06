import { useParams } from 'react-router-dom';
import { useAppHostingEnabled } from '../hooks/useDeploymentMode';
import { useInstance } from '../hooks/useProvisioning';
import { DatabaseServiceCard } from '../components/services/DatabaseServiceCard';
import { ContainersServiceCard } from '../components/services/ContainersServiceCard';

// A project is a container of services (ADR 0035): the overview lists each
// service as a peer with its own status. Containers appear only when the
// server has app hosting on, so a database-only install never sees them.
export function ProjectOverviewPage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const { enabled: appHosting } = useAppHostingEnabled();
  const { data: project } = useInstance(projectId);
  const projectName = project?.projectName || projectId;

  return (
    <div className="max-w-6xl mx-auto space-y-6" data-testid="project-overview">
      <div>
        <h2 className="text-xl font-bold text-text-primary">{projectName}</h2>
        {projectName !== projectId && (
          <p className="text-xs font-mono text-text-tertiary" data-testid="project-overview-id">{projectId}</p>
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
    </div>
  );
}
