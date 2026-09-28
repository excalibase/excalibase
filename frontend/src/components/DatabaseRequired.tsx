import { Link, Outlet, useParams } from 'react-router-dom';
import { Database, Loader2 } from 'lucide-react';
import { useInstance } from '../hooks/useProvisioning';
import { secondaryButton } from './containers/ContainerBits';

// Wraps the pages that work on the project's database (EXC-426). A project
// created without one gets a plain statement and the way to add one, instead
// of each page failing against a database that does not exist. The server
// refuses those calls with 409 regardless; this only saves the round trip.
export function DatabaseRequired() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const { data: project } = useInstance(projectId);

  if (!project) {
    return (
      <div className="flex justify-center py-16">
        <Loader2 className="w-6 h-6 animate-spin text-text-tertiary" />
      </div>
    );
  }
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
