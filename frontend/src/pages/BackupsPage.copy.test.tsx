import { describe, test, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { BackupsPage } from './BackupsPage';

// EXC-667: the Copy tab appears only while the server has dbcopy on.

let dbcopy = false;

vi.mock('../hooks/useDeploymentMode', () => ({
  useFeatureEnabled: (feature: string) => ({ enabled: feature === 'dbcopy' && dbcopy, isLoading: false }),
}));
vi.mock('../hooks/useRouteProjectId', () => ({ useRouteProjectId: () => 'p1' }));
vi.mock('../hooks/useProvisioning', () => ({
  useInstance: () => ({ data: { projectId: 'p1', databaseType: 'POSTGRESQL' } }),
  useListBackups: () => ({ data: { backups: [], backupEnabled: true, schedule: '', retentionDays: 0 }, isLoading: false }),
  useTriggerBackup: () => ({ mutate: vi.fn(), isPending: false }),
  useRestoreFromBackup: () => ({ mutate: vi.fn(), isPending: false, data: undefined }),
  useCopyDatabase: () => ({ mutate: vi.fn(), isPending: false, data: undefined }),
  useRestoreJob: () => ({ data: undefined }),
  RESTORE_RUNNING: 'RUNNING',
}));

describe('BackupsPage copy tab', () => {
  test('is hidden while the feature is off', () => {
    dbcopy = false;
    render(<BackupsPage />);
    expect(screen.queryByRole('button', { name: /^Copy$/i })).not.toBeInTheDocument();
  });

  test('opens the copy form while the feature is on', () => {
    dbcopy = true;
    render(<BackupsPage />);
    fireEvent.click(screen.getByRole('button', { name: /^Copy$/i }));
    expect(screen.getByRole('radio', { name: /As it is now/i })).toBeChecked();
  });
});
