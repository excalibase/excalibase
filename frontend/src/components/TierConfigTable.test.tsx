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
});
