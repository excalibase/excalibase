import { Link, Outlet, useLocation, useParams } from 'react-router-dom';
import { Database, Loader2 } from 'lucide-react';
import { useInstance } from '../hooks/useProvisioning';
import { secondaryButton } from './containers/ContainerBits';
import { apiErrorMessage } from '../api/apps';
import { isMySQLProject } from '../utils/engine';
import { mysqlUnavailableReason } from './layout/navigation';
import { useFeatures, type Feature } from '../hooks/useDeploymentMode';

// Wraps the pages that work on the project's database (EXC-426). A project
// created without one gets a plain statement and the way to add one, instead
// of each page failing against a database that does not exist. The server
// refuses those calls with 409 regardless; this only saves the round trip.
// mysql marks pages that also work on a MySQL project (EXC-586, ADR 0041);
// mysqlFeature, pages that do while that feature is on.
export function DatabaseRequired({ mysql = false, mysqlFeature }: { readonly mysql?: boolean; readonly mysqlFeature?: Feature }) {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const { data: project, error } = useInstance(projectId);
  const features = useFeatures();
  const servesMySQL = mysql || (mysqlFeature !== undefined && features[mysqlFeature] === true);

  if (error && !project) {
    return (
      <p role="alert" className="py-16 text-center text-sm text-red-400">
        {apiErrorMessage(error, 'Could not load the project')}
      </p>
    );
  }
  if (!project) {
    return (
      <div className="flex justify-center py-16">
        <Loader2 className="w-6 h-6 animate-spin text-text-tertiary" />
      </div>
    );
  }
  if (isMySQLProject(project)) return servesMySQL ? <Outlet /> : <PostgresRequired projectId={projectId} />;
  if (!project.noDatabase) return <Outlet />;
  return (
    <div data-testid="database-required" className="max-w-xl mx-auto py-16 text-center space-y-4">
      <Database className="w-10 h-10 mx-auto text-text-tertiary" />
      <h2 className="text-lg font-semibold text-text-primary">This project has no database</h2>
      <p className="text-sm text-text-secondary">
        This page works on the project&apos;s database. Containers, functions and storage run without one.
      </p>
      <Link to={project.canAddDatabase ? `/project/${projectId}/database/add` : `/project/${projectId}`} className={secondaryButton}>
        {project.canAddDatabase ? 'Add database' : 'Back to overview'}
      </Link>
    </div>
  );
}

// The pages behind this guard work on a PostgreSQL database; a MySQL project
// (ADR 0041) is told so, and why when the navigation names a reason.
function PostgresRequired({ projectId }: { readonly projectId: string }) {
  const { pathname } = useLocation();
  const reason = mysqlUnavailableReason(pathname.replace(`/project/${projectId}/`, ''));
  return (
    <div data-testid="postgres-required" className="max-w-xl mx-auto py-16 text-center space-y-4">
      <Database className="w-10 h-10 mx-auto text-text-tertiary" />
      <h2 className="text-lg font-semibold text-text-primary">Not available for MySQL projects</h2>
      <p className="text-sm text-text-secondary">
        This page works on a PostgreSQL database. Connect to your MySQL database with any MySQL client.
      </p>
      {reason && (
        <p className="text-sm text-text-secondary" data-testid="postgres-required-reason">
          {reason}
        </p>
      )}
      <Link to={`/project/${projectId}`} className={secondaryButton}>
        Back to overview
      </Link>
    </div>
  );
}
