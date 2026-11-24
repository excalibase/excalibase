import { describe, test, expect, vi, beforeEach } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { api } from '../../../api/client';
import { DbBackupsTab } from './DbBackupsTab';
import { calledWith, database, plane, renderTab, resetPlane, settle } from './dbTabTestKit';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

const LIST = {
  backupEnabled: true,
  schedule: '0 2 * * *',
  retentionDays: 14,
  backups: [
    { id: 'b-old', timestamp: '2026-10-01T02:00:00Z', status: 'COMPLETED', type: 'SCHEDULED', sizeBytes: 2048 },
    { id: 'b-new', timestamp: '2026-10-03T02:00:00Z', status: 'FAILED', type: 'MANUAL', error: 'disk full' },
  ],
};

function open(extra = {}) {
  plane.routes['/provision/p1/backup/list'] = LIST;
  plane.routes['/provision/p1'] = database(extra);
  return renderTab(<DbBackupsTab project={database(extra)} />);
}

beforeEach(resetPlane);

describe('backups tab', () => {
  test('shows the schedule, retention and the backups newest first', async () => {
    open();
    expect(await screen.findByText('Nightly backups at 02:00 UTC')).toBeInTheDocument();
    expect(screen.getByText('14 days')).toBeInTheDocument();
    const rows = screen.getAllByTestId(/^backup-row-/);
    expect(rows.map((row) => row.dataset.testid)).toEqual(['backup-row-b-new', 'backup-row-b-old']);
    expect(within(rows[0]).getByText(/Failed/)).toBeInTheDocument();
    expect(within(rows[0]).queryByText('disk full')).not.toBeInTheDocument();
    expect(within(rows[1]).getByText(/2\.0 KB/)).toBeInTheDocument();
  });

  test('says so when there are no backups yet', async () => {
    plane.routes['/provision/p1/backup/list'] = { ...LIST, backups: [] };
    plane.routes['/provision/p1'] = database();
    renderTab(<DbBackupsTab project={database()} />);
    expect(await screen.findByText(/No backups yet/)).toBeInTheDocument();
  });

  test('a backup can be started and the answer is shown', async () => {
    open();
    const button = await screen.findByRole('button', { name: 'Back up now' });
    expect(screen.queryByText('Backup started')).not.toBeInTheDocument();
    await userEvent.click(button);
    expect(api.post).toHaveBeenCalledWith('/provision/p1/backup/trigger');
    expect(await screen.findByText('Backup started')).toBeInTheDocument();
  });

  test('a backup the server refuses shows why', async () => {
    open();
    vi.mocked(api.post).mockRejectedValue(Object.assign(new Error('x'), { response: { status: 409, data: { error: 'A backup is already running' } } }));
    await userEvent.click(await screen.findByRole('button', { name: 'Back up now' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('A backup is already running');
  });

  test('a paused database says nightly backups stop and names the final backup', async () => {
    open({ status: 'PAUSED', pauseBackupId: 'b-old' });
    await waitFor(() => expect(screen.getByTestId('paused-backups')).toHaveTextContent('the pause took a final backup b-old'));
  });

  test('a restore into a new project needs a name, then starts', async () => {
    open();
    const start = await screen.findByRole('button', { name: 'Restore Latest Backup' });
    expect(start).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/New project name/), 'orders restored');
    await userEvent.click(start);
    expect(api.post).toHaveBeenCalledWith('/provision/p1/backup/restore', { newProjectName: 'orders restored', targetTime: undefined });
  });

  test('a point in time is offered and changes the restore', async () => {
    open();
    await userEvent.type(await screen.findByLabelText(/Target Time/), '2026-10-02T03:04:05');
    expect(await screen.findByRole('button', { name: 'Restore to Point in Time' })).toBeInTheDocument();
  });

  test('replacing this project stays disabled until the loss is acknowledged', async () => {
    open();
    await userEvent.click(await screen.findByRole('radio', { name: /This project/ }));
    const replace = screen.getByRole('button', { name: "Replace this project's data" });
    expect(replace).toBeDisabled();
    await userEvent.click(screen.getByRole('checkbox'));
    expect(replace).toBeEnabled();
    await userEvent.click(replace);
    expect(api.post).toHaveBeenCalledWith('/provision/p1/backup/restore', expect.objectContaining({ mode: 'in_place', confirmReplace: true }));
  });

  test('copy is offered only when the feature is on', async () => {
    open();
    await settle();
    expect(screen.queryByRole('heading', { name: 'Copy into a new project' })).not.toBeInTheDocument();
  });

  test('copy appears with the feature on', async () => {
    plane.features = { dbcopy: true };
    open();
    expect(await screen.findByRole('heading', { name: 'Copy into a new project' })).toBeInTheDocument();
  });

  test('a MySQL project restores inside its backup window', async () => {
    plane.routes['/provision/p1/backup/list'] = { ...LIST, restoreWindow: { earliest: '2026-10-01T00:00:00Z', latest: '2026-10-03T00:00:00Z' } };
    const mysql = database({ databaseType: 'MYSQL' });
    plane.routes['/provision/p1'] = mysql;
    renderTab(<DbBackupsTab project={mysql} />);
    expect(await screen.findByTestId('restore-window')).toBeInTheDocument();
  });

  test('a developer cannot back up or restore, and is told who can', async () => {
    plane.role = 'developer';
    open();
    expect(await screen.findByText(/Only an admin of this organization can back up or restore/)).toBeInTheDocument();
    await settle();
    expect(screen.queryByRole('button', { name: 'Back up now' })).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/New project name/)).not.toBeInTheDocument();
    expect(calledWith('/backup/list')).toBe(false);
  });

  test('a viewer sees no restore, no trigger and no backup list', async () => {
    plane.role = 'viewer';
    open();
    expect(await screen.findByText(/Only an admin of this organization can back up or restore/)).toBeInTheDocument();
    await settle();
    expect(screen.queryByRole('button', { name: /Restore|Replace|Back up now/ })).not.toBeInTheDocument();
    expect(screen.queryByTestId(/^backup-row-/)).not.toBeInTheDocument();
    expect(calledWith('/backup/list')).toBe(false);
  });

  test('while the role loads an admin sees a spinner, not the admin-only notice', async () => {
    plane.role = 'admin';
    open();
    expect(screen.queryByText(/Only an admin of this organization/)).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Back up now' })).not.toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Back up now' })).toBeEnabled();
    expect(screen.queryByText(/Only an admin of this organization/)).not.toBeInTheDocument();
  });

  test('an admin has the controls', async () => {
    plane.role = 'admin';
    open();
    expect(await screen.findByRole('button', { name: 'Back up now' })).toBeEnabled();
  });
});
