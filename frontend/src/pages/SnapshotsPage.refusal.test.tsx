import { describe, test, expect, beforeEach, afterEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SnapshotsPage } from './SnapshotsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const snapshot = {
  id: 'p1-20261001-120000', projectId: 'p1', format: 'custom', size: 2048,
  createdAt: '2026-10-01T12:00:00Z', documents: false, schemaOnly: false, dataOnly: false,
};

const refusal = (reason: string) => ({
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: reason } },
});

function renderPage() {
  vi.mocked(api.get).mockResolvedValue({ data: [snapshot] } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p1/snapshots']}>
        <Routes>
          <Route path="/project/:projectId/snapshots" element={<SnapshotsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('SnapshotsPage refusals', () => {
  let confirm: ReturnType<typeof vi.spyOn>;
  beforeEach(() => {
    vi.clearAllMocks();
    confirm = vi.spyOn(globalThis, 'confirm').mockReturnValue(true);
  });
  afterEach(() => confirm.mockRestore());

  test("a refused export shows the server's reason", async () => {
    vi.mocked(api.post).mockRejectedValueOnce(refusal('a snapshot is already running'));
    renderPage();
    await userEvent.click(screen.getByRole('button', { name: /Export Snapshot/ }));

    expect(await screen.findByText('a snapshot is already running')).toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test("a refused delete shows the server's reason", async () => {
    vi.mocked(api.delete).mockRejectedValueOnce(refusal('the snapshot is being downloaded'));
    renderPage();
    await userEvent.click(await screen.findByRole('button', { name: /Delete/ }));

    expect(await screen.findByText('the snapshot is being downloaded')).toBeInTheDocument();
    expect(api.delete).toHaveBeenCalledWith(`/provision/p1/snapshot/${snapshot.id}`);
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });
});
