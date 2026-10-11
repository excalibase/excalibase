import { describe, test, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { BackupsPage } from './BackupsPage';

const WINDOW = { earliest: '2026-10-11T00:00:00Z', latest: '2026-10-11T12:00:00Z' };
const BACKUPS = [
  { id: '20261011-060000', timestamp: '2026-10-11T06:00:00Z', type: 'SCHEDULED', status: 'COMPLETED', sizeBytes: 4608 },
  { id: '20261011-120000', timestamp: '2026-10-11T12:00:00Z', type: 'MANUAL', status: 'COMPLETED', sizeBytes: 3 * 1024 * 1024 },
  { id: 'moco-backup-x-manual-1', timestamp: '2026-10-11T13:00:00Z', type: 'MANUAL', status: 'IN_PROGRESS' },
];

vi.mock('../hooks/useRouteProjectId', () => ({ useRouteProjectId: () => 'my1' }));
vi.mock('../hooks/useProvisioning', () => ({
  useInstance: () => ({ data: { projectId: 'my1', databaseType: 'MYSQL' } }),
  useListBackups: () => ({
    data: { backups: BACKUPS, backupEnabled: true, schedule: '0 */6 * * *', retentionDays: 7, restoreWindow: WINDOW },
    isLoading: false,
  }),
  useTriggerBackup: () => ({ mutate: vi.fn(), isPending: false }),
  useRestoreFromBackup: () => ({ mutate: vi.fn(), isPending: false, data: undefined }),
  useRestoreJob: () => ({ data: undefined }),
  RESTORE_RUNNING: 'RUNNING',
}));

// EXC-586: a MySQL project's Backups page restores to a time inside its window.
describe('BackupsPage on a MySQL project', () => {
  test('the history shows each backup\'s size and whether it was manual', () => {
    render(<BackupsPage />);
    const scheduled = screen.getByText('20261011-060000').closest('tr')!;
    expect(scheduled).toHaveTextContent('SCHEDULED');
    expect(scheduled).toHaveTextContent('4.5 KB');
    const manual = screen.getByText('20261011-120000').closest('tr')!;
    expect(manual).toHaveTextContent('MANUAL');
    expect(manual).toHaveTextContent('3.0 MB');
    expect(screen.getByText('moco-backup-x-manual-1').closest('tr')!).toHaveTextContent('--');
  });

  test('the restore tab names the backup window', () => {
    render(<BackupsPage />);
    fireEvent.click(screen.getByRole('button', { name: /Restore/i }));
    expect(screen.getByTestId('restore-window')).toHaveTextContent('Point-in-time within the backup window:');
    expect(screen.queryByText(/PITR/)).not.toBeInTheDocument();
  });
});
