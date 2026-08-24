import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { RealtimePage } from './RealtimePage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: {
    get: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
    post: vi.fn(),
  },
}));

function renderPage(initialTables: Array<{ schema: string; table: string; enabled: boolean }>) {
  const state = { tables: [...initialTables] };

  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url.endsWith('/realtime/tables')) {
      return Promise.resolve({ data: state.tables } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  vi.mocked(api.put).mockImplementation((url: string) => {
    const m = url.match(/realtime\/tables\/([^/]+)\/([^/]+)$/);
    if (m) {
      const target = state.tables.find((t) => t.schema === m[1] && t.table === m[2]);
      if (target) target.enabled = true;
      return Promise.resolve({ data: target ?? null } as never);
    }
    return Promise.reject(new Error('unexpected PUT'));
  });
  vi.mocked(api.delete).mockImplementation((url: string) => {
    const m = url.match(/realtime\/tables\/([^/]+)\/([^/]+)$/);
    if (m) {
      const target = state.tables.find((t) => t.schema === m[1] && t.table === m[2]);
      if (target) target.enabled = false;
      return Promise.resolve({ data: target ?? null } as never);
    }
    return Promise.reject(new Error('unexpected DELETE'));
  });
  vi.mocked(api.post).mockImplementation((url: string) => {
    if (url.endsWith('/enable-all')) {
      let added = 0;
      for (const t of state.tables) {
        if (!t.enabled) {
          t.enabled = true;
          added++;
        }
      }
      return Promise.resolve({ data: { added } } as never);
    }
    if (url.endsWith('/disable-all')) {
      let dropped = 0;
      for (const t of state.tables) {
        if (t.enabled) {
          t.enabled = false;
          dropped++;
        }
      }
      return Promise.resolve({ data: { dropped } } as never);
    }
    return Promise.reject(new Error(`unexpected POST ${url}`));
  });

  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-abc/realtime']}>
        <Routes>
          <Route path="/project/:projectId/realtime" element={<RealtimePage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('RealtimePage', () => {
  beforeEach(() => vi.clearAllMocks());

  test('renders tables with current enabled state and counter', async () => {
    renderPage([
      { schema: 'public', table: 'posts', enabled: true },
      { schema: 'public', table: 'comments', enabled: false },
      { schema: 'nosql', table: 'notes', enabled: false },
    ]);

    expect(await screen.findByTestId('realtime-row-public-posts')).toBeInTheDocument();
    expect(screen.getByTestId('realtime-row-public-comments')).toBeInTheDocument();
    expect(screen.getByTestId('realtime-row-nosql-notes')).toBeInTheDocument();
    expect(screen.getByTestId('realtime-counter')).toHaveTextContent(/1.*3/);
  });

  test('per-row toggle calls PUT and flips checkbox state', async () => {
    const user = userEvent.setup();
    renderPage([{ schema: 'public', table: 'posts', enabled: false }]);

    const toggle = await screen.findByTestId('realtime-toggle-public-posts');
    expect(toggle).not.toBeChecked();

    await user.click(toggle);
    await waitFor(() => expect(api.put).toHaveBeenCalledWith('/projects/proj-abc/realtime/tables/public/posts'));
    await waitFor(() => expect(toggle).toBeChecked());
  });

  test('per-row toggle off calls DELETE', async () => {
    const user = userEvent.setup();
    renderPage([{ schema: 'public', table: 'posts', enabled: true }]);

    const toggle = await screen.findByTestId('realtime-toggle-public-posts');
    await waitFor(() => expect(toggle).toBeChecked());

    await user.click(toggle);
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/projects/proj-abc/realtime/tables/public/posts'));
  });

  test('search filters the visible rows', async () => {
    const user = userEvent.setup();
    renderPage([
      { schema: 'public', table: 'posts', enabled: false },
      { schema: 'nosql', table: 'notes', enabled: false },
    ]);

    await screen.findByTestId('realtime-row-public-posts');
    await user.type(screen.getByTestId('realtime-search'), 'note');

    await waitFor(() => {
      expect(screen.queryByTestId('realtime-row-public-posts')).not.toBeInTheDocument();
      expect(screen.getByTestId('realtime-row-nosql-notes')).toBeInTheDocument();
    });
  });

  test('show-only-enabled checkbox filters disabled rows out', async () => {
    const user = userEvent.setup();
    renderPage([
      { schema: 'public', table: 'posts', enabled: true },
      { schema: 'public', table: 'comments', enabled: false },
    ]);

    await screen.findByTestId('realtime-row-public-comments');
    await user.click(screen.getByTestId('realtime-filter-enabled'));

    await waitFor(() => {
      expect(screen.queryByTestId('realtime-row-public-comments')).not.toBeInTheDocument();
      expect(screen.getByTestId('realtime-row-public-posts')).toBeInTheDocument();
    });
  });

  test('Enable all opens confirmation modal then bulk-enables', async () => {
    const user = userEvent.setup();
    renderPage([
      { schema: 'public', table: 'posts', enabled: false },
      { schema: 'public', table: 'comments', enabled: false },
    ]);

    await screen.findByTestId('realtime-row-public-posts');
    await user.click(screen.getByTestId('realtime-enable-all'));

    // Modal opens
    expect(await screen.findByText(/Enable realtime for all tables/i)).toBeInTheDocument();
    // Confirm via the modal's Enable all button
    const allEnableBtns = screen.getAllByRole('button', { name: 'Enable all' });
    await user.click(allEnableBtns[allEnableBtns.length - 1]);

    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/projects/proj-abc/realtime/enable-all'));
    await waitFor(() => {
      expect(screen.getByTestId('realtime-toggle-public-posts')).toBeChecked();
      expect(screen.getByTestId('realtime-toggle-public-comments')).toBeChecked();
    });
  });

  test('Disable all confirmation flow drops everything', async () => {
    const user = userEvent.setup();
    renderPage([
      { schema: 'public', table: 'posts', enabled: true },
      { schema: 'public', table: 'comments', enabled: true },
    ]);

    await screen.findByTestId('realtime-row-public-posts');
    await user.click(screen.getByTestId('realtime-disable-all'));

    expect(await screen.findByText(/Disable realtime for all tables/i)).toBeInTheDocument();
    const allDisableBtns = screen.getAllByRole('button', { name: 'Disable all' });
    await user.click(allDisableBtns[allDisableBtns.length - 1]);

    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/projects/proj-abc/realtime/disable-all'));
  });

  test('renders empty state when no tables exist yet', async () => {
    renderPage([]);
    await waitFor(() => {
      expect(screen.getByText(/No user tables yet/i)).toBeInTheDocument();
    });
  });
});
