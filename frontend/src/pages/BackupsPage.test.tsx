import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { BackupsPage } from './BackupsPage';

const restoreMutate = vi.fn();

vi.mock('../context/InstanceContext', () => ({ useInstanceContext: () => ({ projectId: 'p1' }) }));
vi.mock('../hooks/useProvisioning', () => ({
  useListBackups: () => ({ data: { backups: [], backupEnabled: true, schedule: '', retentionDays: 0 }, isLoading: false }),
  useTriggerBackup: () => ({ mutate: vi.fn(), isPending: false }),
  useRestoreFromBackup: () => ({ mutate: restoreMutate, isPending: false, data: undefined }),
}));

beforeEach(() => restoreMutate.mockReset());

function openRestore(name: string) {
  render(<BackupsPage />);
  fireEvent.click(screen.getByRole('button', { name: /Restore \/ PITR/i }));
  fireEvent.change(screen.getByLabelText(/New Instance Name/i), { target: { value: name } });
}

describe('BackupsPage restore', () => {
  test('a point-in-time restore sends the local time shown as a zoned instant', () => {
    openRestore('copy');
    fireEvent.change(screen.getByLabelText(/Target Time \(PITR\)/i), { target: { value: '2026-05-04T03:30:15' } });
    fireEvent.click(screen.getByRole('button', { name: /Restore to Point in Time/i }));

    expect(restoreMutate).toHaveBeenCalledTimes(1);
    expect(restoreMutate.mock.calls[0][0]).toEqual({
      newProjectName: 'copy',
      targetTime: new Date(2026, 4, 4, 3, 30, 15).toISOString(),
    });
  });

  test('a restore without a time sends no target', () => {
    openRestore('copy');
    fireEvent.click(screen.getByRole('button', { name: /Restore Latest Backup/i }));

    expect(restoreMutate.mock.calls[0][0]).toEqual({ newProjectName: 'copy', targetTime: undefined });
  });
});
