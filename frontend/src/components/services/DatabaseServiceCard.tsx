import { Link } from 'react-router-dom';
import { Database, Loader2 } from 'lucide-react';
import { useInstance } from '../../hooks/useProvisioning';
import { useProjectEndpoint } from '../../api/projectEndpoint';
import { apiErrorMessage } from '../../api/apps';
import { ToneBadge, secondaryButton } from '../containers/ContainerBits';
import { engineLabel, isMySQLProject } from '../../utils/engine';
import { databaseServiceStatus } from './serviceStatus';
import type { DatabaseInstance } from '../../types';
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

// A project created without a database (EXC-426). The add control shows only
// when the server says this caller may use it; the route decides regardless.
function NoDatabase({ project }: { readonly project: DatabaseInstance }) {
  if (project.status === 'PROVISIONING') {
    return (
      <p data-testid="service-database-adding" className="text-sm text-text-secondary">
        The database is being added. This usually takes about a minute; this card updates when it is ready
        or names what went wrong.
      </p>
    );
  }
  return (
    <>
      <p data-testid="service-database-empty" className="text-sm text-text-secondary">
        This project has no database. Add one to get a managed PostgreSQL (or DocumentDB) database with its
        GraphQL and REST API, sized by your organization&apos;s plan.
        {!project.canAddDatabase && ' Only an org admin or owner can add one.'}
      </p>
      {project.failureReason && (
        <p className="text-xs text-red-400 break-words">Last attempt failed: {project.failureReason}</p>
      )}
      {project.canAddDatabase && (
        <div>
          <Link to={`/project/${project.projectId}/database/add`} className={secondaryButton}>
            Add database
          </Link>
        </div>
      )}
    </>
  );
}

function projectVersion(project: DatabaseInstance): string {
  const version = isMySQLProject(project) ? project.mysqlVersion : project.postgresVersion;
  return version ? ` ${version}` : '';
}

// Where a MySQL project's database answers inside the platform (ADR 0041).
// Every connection must use TLS; the server refuses one without it.
function MySQLEndpoint({ project }: { readonly project: DatabaseInstance }) {
  return (
    <AccessNote testId="service-database-mysql-endpoint">
      Reachable inside this project at{' '}
      <code className="font-mono">
        {project.host}:{project.port}
      </code>
      , database <code className="font-mono">{project.databaseName}</code>. TLS required.
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
      description={
        isMySQLProject(project)
          ? 'A managed MySQL database.'
          : 'A managed database with its generated GraphQL and REST API.'
      }
      badge={status && <ToneBadge {...status} testId="service-database-status" />}
    >
      {!project && !error && <Loader2 className="w-5 h-5 animate-spin text-text-tertiary" />}
      {error && (
        <p role="alert" className="text-sm text-red-400">
          {apiErrorMessage(error, 'Could not load the database')}
        </p>
      )}
      {project?.noDatabase && <NoDatabase project={project} />}
      {project && !project.noDatabase && (
        <>
          <dl className="grid grid-cols-2 gap-3 text-sm">
            <div>
              <dt className="text-xs text-text-tertiary">Engine</dt>
              <dd className="text-text-primary">
                {engineLabel(project)}
                {projectVersion(project)}
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
          {isMySQLProject(project) ? (
            <MySQLEndpoint project={project} />
          ) : (
            <>
              <div>
                <Link to={`/project/${projectId}/database/overview`} className={secondaryButton}>
                  Open database
                </Link>
              </div>
              <DatabaseAccess projectId={projectId} />
            </>
          )}
        </>
      )}
    </ServiceCard>
  );
}
