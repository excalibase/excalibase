import { Link, Outlet, useParams } from 'react-router-dom';
import { Database, Loader2 } from 'lucide-react';
import { useInstance } from '../hooks/useProvisioning';
import { secondaryButton } from './containers/ContainerBits';

interface NotRunning {
  readonly title: string;
  readonly body: string;
  // The state ends by itself; the page opens once it does (useInstance polls).
  readonly waits: boolean;
}

const OPENS_ITSELF = 'This page opens by itself once the database is ready.';

const STATES: Readonly<Record<string, NotRunning>> = {
  PROVISIONING: { title: 'Your database is being set up', body: `${OPENS_ITSELF} It usually takes a few minutes.`, waits: true },
  PAUSING: { title: 'This project is pausing', body: 'Its database stops serving while it is paused. Resume it from the overview.', waits: true },
  PAUSED: { title: 'This project is paused', body: 'Its database is not running. Resume the project from the overview to use this page.', waits: false },
  RESUMING: { title: 'This project is resuming', body: OPENS_ITSELF, waits: true },
  RESTORING: { title: 'The database is being restored', body: OPENS_ITSELF, waits: true },
  PENDING_DELETION: { title: 'This project is scheduled for deletion', body: 'Its database is no longer served. The overview shows when it will be removed.', waits: false },
  FAILED: { title: 'The database could not be created', body: 'The overview shows which step failed.', waits: false },
  DELETING: { title: 'This project is being deleted', body: 'Its database is no longer served.', waits: false },
  DEPROVISIONED: { title: 'This project has been deleted', body: 'Its database is no longer served.', waits: false },
  BACKUPS_PENDING_DELETE: { title: 'This project has been deleted', body: 'Its database is no longer served.', waits: false },
};

function notRunning(status: string): NotRunning {
  const known = STATES[status === '' ? 'PROVISIONING' : status];
  if (known) return known;
  return {
    title: `The database is not running (${status})`,
    body: 'The overview shows the project’s state.',
    waits: false,
  };
}

// Wraps the pages that connect to the project's database. The server serves
// only an ACTIVE project's database (projectdb.checkServable) and answers 409
// otherwise; this says why instead of leaving each page loading.
export function DatabaseRunning() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const { data: project } = useInstance(projectId);

  // Loading and load errors are DatabaseRequired's, which wraps this.
  if (!project || project.status === 'ACTIVE') return <Outlet />;
  const state = notRunning(project.status ?? '');
  return (
    <div data-testid="database-not-running" className="max-w-xl mx-auto py-16 text-center space-y-4">
      {state.waits ? (
        <Loader2 className="w-10 h-10 mx-auto text-accent-primary animate-spin" />
      ) : (
        <Database className="w-10 h-10 mx-auto text-text-tertiary" />
      )}
      <h2 className="text-lg font-semibold text-text-primary">{state.title}</h2>
      <p className="text-sm text-text-secondary">{state.body}</p>
      <Link to={`/project/${projectId}`} className={secondaryButton}>
        Project overview
      </Link>
    </div>
  );
}
