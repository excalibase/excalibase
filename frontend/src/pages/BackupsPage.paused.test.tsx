import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { BackupsPage } from './BackupsPage';

let instance: Record<string, unknown> = {};
const failedNights = Array.from({ length: 3 }, (_, i) => ({
  id: `night-${i}`, timestamp: `2026-11-0${i + 2}T02:00:00Z`, type: 'SCHEDULED', status: 'FAILED',
  error: 'cannot backup a hibernated cluster',
}));
const FINAL = { id: 'backup-final-1', timestamp: '2026-11-01T01:00:00Z', type: 'MANUAL', status: 'COMPLETED' };
const EARLIER = { id: 'backup-old', timestamp: '2026-10-20T02:00:00Z', type: 'SCHEDULED', status: 'COMPLETED' };
let backups = [EARLIER, FINAL, ...failedNights];

vi.mock('../hooks/useDeploymentMode', () => ({ useFeatureEnabled: () => ({ enabled: false, isLoading: false }) }));
vi.mock('../hooks/useRouteProjectId', () => ({ useRouteProjectId: () => 'p1' }));
vi.mock('../hooks/useProvisioning', () => ({
  useInstance: () => ({ data: instance }),
  useListBackups: () => ({ data: { backups, backupEnabled: true, schedule: '0 2 * * *', retentionDays: 7 }, isLoading: false }),
  useTriggerBackup: () => ({ mutate: vi.fn(), isPending: false }),
  useRestoreFromBackup: () => ({ mutate: vi.fn(), isPending: false, data: undefined }),
  useRestoreJob: () => ({ data: undefined }),
  RESTORE_RUNNING: 'RUNNING',
}));

// EXC-758: a paused project takes no nightly backups; say so instead of listing failures.
describe('BackupsPage on a paused project', () => {
  test('names the final backup only when it is the pause backup', () => {
    backups = [EARLIER, FINAL, ...failedNights];
    instance = { projectId: 'p1', databaseType: 'POSTGRESQL', status: 'PAUSED', pauseBackupId: 'backup-final-1' };
    render(<BackupsPage />);
    expect(screen.getByTestId('paused-backups')).toHaveTextContent(
      'Paused: nightly backups stop while paused; the pause took a final backup backup-final-1',
    );
  });

  test('otherwise calls the newest completed backup the latest completed backup', () => {
    backups = [EARLIER, FINAL, ...failedNights];
    instance = { projectId: 'p1', databaseType: 'POSTGRESQL', status: 'PAUSED' };
    render(<BackupsPage />);
    const note = screen.getByTestId('paused-backups');
    expect(note).toHaveTextContent('the latest completed backup is backup-final-1');
    expect(note).not.toHaveTextContent('final backup');
  });

  test('still lists a failure that is not the hibernated-cluster kind', () => {
    backups = [EARLIER, { id: 'store-down', timestamp: '2026-11-05T02:00:00Z', type: 'SCHEDULED', status: 'FAILED', error: 'bucket unreachable' }, ...failedNights];
    instance = { projectId: 'p1', databaseType: 'POSTGRESQL', status: 'PAUSED' };
    render(<BackupsPage />);
    expect(screen.getByText('store-down')).toBeInTheDocument();
    expect(screen.queryByText('night-0')).not.toBeInTheDocument();
  });

  test('does not list a run of failed backups while paused', () => {
    backups = [EARLIER, FINAL, ...failedNights];
    instance = { projectId: 'p1', databaseType: 'POSTGRESQL', status: 'PAUSED' };
    render(<BackupsPage />);
    expect(screen.queryByText('night-0')).not.toBeInTheDocument();
    expect(screen.getByText('backup-old')).toBeInTheDocument();
  });

  test('without any completed backup it still says the nightly backups stop', () => {
    instance = { projectId: 'p1', databaseType: 'POSTGRESQL', status: 'PAUSED' };
    backups = [...failedNights];
    render(<BackupsPage />);
    expect(screen.getByTestId('paused-backups')).toHaveTextContent('Paused: nightly backups stop while paused');
    expect(screen.getByTestId('paused-backups')).not.toHaveTextContent('completed backup');
  });

  test('a running project lists its backups and shows no paused note', () => {
    instance = { projectId: 'p1', databaseType: 'POSTGRESQL', status: 'ACTIVE' };
    backups = [EARLIER, FINAL, ...failedNights];
    render(<BackupsPage />);
    expect(screen.queryByTestId('paused-backups')).not.toBeInTheDocument();
    expect(screen.getByText('night-0')).toBeInTheDocument();
  });
});
