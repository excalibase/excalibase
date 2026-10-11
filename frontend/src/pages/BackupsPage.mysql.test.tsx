import { describe, test, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { BackupsPage } from './BackupsPage';

const WINDOW = { earliest: '2026-10-11T00:00:00Z', latest: '2026-10-11T12:00:00Z' };

vi.mock('../hooks/useRouteProjectId', () => ({ useRouteProjectId: () => 'my1' }));
vi.mock('../hooks/useProvisioning', () => ({
  useInstance: () => ({ data: { projectId: 'my1', databaseType: 'MYSQL' } }),
  useListBackups: () => ({
    data: { backups: [], backupEnabled: true, schedule: '0 */6 * * *', retentionDays: 7, restoreWindow: WINDOW },
    isLoading: false,
  }),
  useTriggerBackup: () => ({ mutate: vi.fn(), isPending: false }),
  useRestoreFromBackup: () => ({ mutate: vi.fn(), isPending: false, data: undefined }),
  useRestoreJob: () => ({ data: undefined }),
  RESTORE_RUNNING: 'RUNNING',
}));

// EXC-586: a MySQL project's Backups page restores to a time inside its window.
describe('BackupsPage on a MySQL project', () => {
  test('the restore tab names the backup window', () => {
    render(<BackupsPage />);
    fireEvent.click(screen.getByRole('button', { name: /Restore/i }));
    expect(screen.getByTestId('restore-window')).toHaveTextContent('Point-in-time within the backup window:');
    expect(screen.queryByText(/PITR/)).not.toBeInTheDocument();
  });
});
