import { Link } from 'react-router-dom';
import { Database, Loader2 } from 'lucide-react';
import { useInstance } from '../../hooks/useProvisioning';
import { useProjectEndpoint } from '../../api/projectEndpoint';
import { apiErrorMessage } from '../../api/apps';
import { ToneBadge, secondaryButton } from '../containers/ContainerBits';
import { engineLabel } from '../../utils/engine';
import { databaseServiceStatus } from './serviceStatus';
import { AccessNote, ServiceCard } from './ServiceCard';

function DatabaseAccess({ projectId }: { readonly projectId: string }) {
  const { data: endpoint } = useProjectEndpoint(projectId);
  const settings = (
    <Link to={`/project/${projectId}/settings`} className="text-purple-400 hover:underline">
      Settings
    </Link>
  );
  if (endpoint?.publicEnabled) {
    return (
      <AccessNote testId="service-database-access">
        Public port open at{' '}
        <code className="font-mono">
          {endpoint.host}:{endpoint.port}
        </code>{' '}
        (opened by an admin; private by default). Close it in {settings}.
      </AccessNote>
    );
  }
  return (
    <AccessNote testId="service-database-access">
      Private by default: reachable only from inside this project. A public port is opt-in, for
      tools outside the platform; an admin can open one in {settings}.
    </AccessNote>
  );
}

export function DatabaseServiceCard({ projectId }: { readonly projectId: string }) {
  const { data: project, error } = useInstance(projectId);
  const status = project ? databaseServiceStatus(project) : undefined;

  return (
    <ServiceCard
      testId="service-database"
      icon={Database}
      title="Database"
      description="A managed database with its generated GraphQL and REST API."
      badge={status && <ToneBadge {...status} testId="service-database-status" />}
    >
      {!project && !error && <Loader2 className="w-5 h-5 animate-spin text-text-tertiary" />}
      {error && (
        <p role="alert" className="text-sm text-red-400">
          {apiErrorMessage(error, 'Could not load the database')}
        </p>
      )}
      {project && (
        <>
          <dl className="grid grid-cols-2 gap-3 text-sm">
            <div>
              <dt className="text-xs text-text-tertiary">Engine</dt>
              <dd className="text-text-primary">
                {engineLabel(project)}
                {project.postgresVersion ? ` ${project.postgresVersion}` : ''}
              </dd>
            </div>
            <div>
              <dt className="text-xs text-text-tertiary">Plan</dt>
              <dd className="text-text-primary">{project.tier}</dd>
            </div>
          </dl>
          {project.failureReason && project.currentStage === 'FAILED' && (
            <p className="text-xs text-red-400 break-words">{project.failureReason}</p>
          )}
          <div>
            <Link to={`/project/${projectId}/database/overview`} className={secondaryButton}>
              Open database
            </Link>
          </div>
          <DatabaseAccess projectId={projectId} />
        </>
      )}
    </ServiceCard>
  );
}
