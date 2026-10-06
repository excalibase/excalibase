import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { BackupsPage } from './BackupsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

describe('BackupsPage restore refusal', () => {
  test("a refused restore shows the server's reason", async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { backups: [], backupEnabled: true, schedule: '', retentionDays: 7 } } as never);
    vi.mocked(api.post).mockRejectedValueOnce({
      message: 'Request failed with status code 409',
      response: { status: 409, data: { error: 'a project named copy already exists' } },
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/project/p1/backups']}>
          <Routes>
            <Route path="/project/:projectId/backups" element={<BackupsPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await userEvent.click(screen.getByRole('button', { name: /Restore \/ PITR/i }));
    await userEvent.type(screen.getByLabelText(/New Instance Name/i), 'copy');
    await userEvent.click(screen.getByRole('button', { name: /Restore Latest Backup/i }));

    expect(await screen.findByText('a project named copy already exists')).toBeInTheDocument();
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
    expect(api.post).toHaveBeenCalledWith('/provision/p1/backup/restore', { newProjectName: 'copy', targetTime: undefined });
  });
});
