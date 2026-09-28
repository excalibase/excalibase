import type { DatabaseInstance } from '../../types';
import type { Tone } from '../containers/appCopy';

export interface ServiceStatus {
  label: string;
  tone: Tone;
}

// The database's own lifecycle status. Nothing here looks at the containers:
// each service on the project overview reports only its own state.
const DATABASE_STATUS: Record<string, ServiceStatus> = {
  ACTIVE: { label: 'Running', tone: 'success' },
  PROVISIONING: { label: 'Provisioning', tone: 'progress' },
  FAILED: { label: 'Failed', tone: 'error' },
  PAUSED: { label: 'Paused', tone: 'warning' },
  PAUSING: { label: 'Pausing', tone: 'progress' },
  RESUMING: { label: 'Resuming', tone: 'progress' },
  RESTORING: { label: 'Restoring', tone: 'progress' },
  PENDING_DELETION: { label: 'Scheduled for deletion', tone: 'warning' },
  DELETING: { label: 'Deleting', tone: 'progress' },
};

export function databaseServiceStatus(
  project: Pick<DatabaseInstance, 'status' | 'currentStage' | 'noDatabase'>,
): ServiceStatus {
  // A project created without a database (EXC-426) is running; it just has
  // no database service to report on.
  if (project.noDatabase && project.status === 'ACTIVE') return { label: 'No database', tone: 'neutral' };
  if (project.currentStage === 'FAILED') return DATABASE_STATUS.FAILED;
  return DATABASE_STATUS[project.status] ?? { label: project.status, tone: 'neutral' };
}

export function containersSummary(apps: ReadonlyArray<{ id: string }>): string {
  if (apps.length === 0) return 'No containers yet';
  return apps.length === 1 ? '1 container' : `${apps.length} containers`;
}
