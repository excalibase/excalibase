import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { TierConfigTable } from './TierConfigTable';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), put: vi.fn() } }));

const standard = {
  tier: 'STANDARD',
  maxProjects: 5,
  instances: 3,
  storageSize: '50Gi',
  maxStorageSize: '500Gi',
  maxAppDiskSize: '20Gi',
  maxApps: 5,
  memory: '4Gi',
  cpu: '2',
  backupEnabled: true,
  autoPauseAfterDays: 0,
};

function renderTable() {
  vi.mocked(api.get).mockResolvedValue({ data: [standard] } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <TierConfigTable canMutate />
    </QueryClientProvider>,
  );
}

describe('TierConfigTable', () => {
  beforeEach(() => vi.clearAllMocks());

  // A plan's disk has a starting size and a maximum a project may grow to (EXC-492).
  test('edits and saves the maximum disk with the rest of the plan', async () => {
    const user = userEvent.setup();
    vi.mocked(api.put).mockResolvedValueOnce({ data: standard } as never);
    renderTable();

    const max = await screen.findByDisplayValue('500Gi');
    await user.clear(max);
    await user.type(max, '1Ti');
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        '/admin/tiers/STANDARD',
        expect.objectContaining({ storageSize: '50Gi', maxStorageSize: '1Ti' }),
      ),
    );
  });

  // The largest disk one container may have on the plan (EXC-523).
  test('edits and saves the app disk cap with the rest of the plan', async () => {
    const user = userEvent.setup();
    vi.mocked(api.put).mockResolvedValueOnce({ data: standard } as never);
    renderTable();

    const cap = await screen.findByDisplayValue('20Gi');
    await user.clear(cap);
    await user.type(cap, '40Gi');
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        '/admin/tiers/STANDARD',
        expect.objectContaining({ maxStorageSize: '500Gi', maxAppDiskSize: '40Gi' }),
      ),
    );
  });

  // How many containers one project may hold on the plan (EXC-524).
  test('edits and saves the app count with the rest of the plan', async () => {
    const user = userEvent.setup();
    vi.mocked(api.put).mockResolvedValueOnce({ data: standard } as never);
    renderTable();

    const count = await screen.findByLabelText('Max apps for STANDARD');
    await user.clear(count);
    await user.type(count, '8');
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/admin/tiers/STANDARD', expect.objectContaining({ maxApps: 8, maxAppDiskSize: '20Gi' })),
    );
  });

  // Small plans may cap app disks in mebibytes (EXC-523).
  test('saves an app disk cap in Mi and explains the accepted units', async () => {
    const user = userEvent.setup();
    renderTable();

    const cap = await screen.findByDisplayValue('20Gi');
    await user.clear(cap);
    await user.type(cap, '500Mi');
    await user.click(screen.getByRole('button', { name: /save/i }));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(
        '/admin/tiers/STANDARD',
        expect.objectContaining({ maxAppDiskSize: '500Mi' }),
      ),
    );
    expect(screen.getByRole('columnheader', { name: /max app disk/i })).toHaveAttribute(
      'title',
      expect.stringMatching(/Mi.*Gi.*0Gi = none/),
    );
  });

  test('an app disk cap that is not whole Mi or Gi is not saved', async () => {
    const user = userEvent.setup();
    renderTable();

    const cap = await screen.findByDisplayValue('20Gi');
    await user.clear(cap);
    await user.type(cap, '1.5GB');

    expect(screen.getByRole('button', { name: /save/i })).toBeDisabled();
    expect(screen.getByTestId('app-disk-cap-error-STANDARD')).toHaveTextContent(/Mi or.*Gi/);
  });
});
