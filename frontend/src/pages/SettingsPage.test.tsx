import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SettingsPage } from './SettingsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

const RESPOND_ASYNC = { headers: { Prefer: 'respond-async' } };

function renderSettings(
  deletionProtection: boolean,
  extra: Record<string, unknown> = {},
  otherGets: Record<string, unknown> = {},
) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url in otherGets) return Promise.resolve({ data: otherGets[url] } as never);
    if (url === '/provision/p-1') {
      return Promise.resolve({
        data: { projectId: 'p-1', orgId: 'o-1', databaseType: 'POSTGRESQL', tier: 'FREE', status: 'ACTIVE', deletionProtection, ...extra },
      } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  vi.mocked(api.post).mockResolvedValue({ data: { status: 'PAUSED' } } as never);
  vi.mocked(api.patch).mockResolvedValue({ data: { deletionProtection: !deletionProtection } } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p-1/settings']}>
        <Routes>
          <Route path="/project/:projectId/settings" element={<SettingsPage />} />
          <Route path="/instances" element={<div data-testid="projects-list-page" />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>
  );
}

describe('SettingsPage — deletion protection', () => {
  beforeEach(() => vi.clearAllMocks());

  test('a protected project cannot be deleted until protection is turned off', async () => {
    renderSettings(true);
    expect(await screen.findByTestId('delete-project-btn')).toBeDisabled();

    await userEvent.click(screen.getByTestId('deletion-protection-btn'));
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith('/provision/p-1/deletion-protection', { enabled: false })
    );
  });

  test('an unprotected project can be deleted and protection turned back on', async () => {
    renderSettings(false);
    expect(await screen.findByTestId('delete-project-btn')).toBeEnabled();

    await userEvent.click(screen.getByTestId('deletion-protection-btn'));
    await waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith('/provision/p-1/deletion-protection', { enabled: true })
    );
  });

  test('deleting says the project is kept for 7 days first', async () => {
    renderSettings(false);
    expect(await screen.findByTestId('deletion-grace-note')).toHaveTextContent(/7 days/);
  });

  test('deleting says the containers go offline at once', async () => {
    renderSettings(false);
    expect(await screen.findByTestId('deletion-grace-note')).toHaveTextContent(/containers.*offline now/i);
  });

  test('a project scheduled for deletion says its containers are stopped until resumed', async () => {
    renderSettings(false, { status: 'PENDING_DELETION', deletionDueAt: '2026-10-05T10:00:00Z' });
    expect(await screen.findByTestId('deletion-scheduled')).toHaveTextContent(/containers are stopped.*resume/i);
  });

  test('a project scheduled for deletion shows when and can be cancelled', async () => {
    renderSettings(false, { status: 'PENDING_DELETION', deletionDueAt: '2026-10-05T10:00:00Z' });
    expect(await screen.findByTestId('deletion-scheduled')).toHaveTextContent(/scheduled for deletion/i);
    expect(screen.queryByTestId('delete-project-btn')).toBeNull();

    await userEvent.click(screen.getByTestId('cancel-deletion-btn'));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/provision/p-1/deletion/cancel'));
  });

  test('a restore refused because the plan is full shows why', async () => {
    renderSettings(false, { status: 'PENDING_DELETION', deletionDueAt: '2026-10-05T10:00:00Z' });
    const refusal =
      'Your plan allows 1 project(s) and they are in use; delete one or move to a larger plan before restoring this project';
    vi.mocked(api.post).mockRejectedValueOnce({ response: { status: 409, data: { error: refusal } } });

    await userEvent.click(await screen.findByTestId('cancel-deletion-btn'));
    expect(await screen.findByTestId('cancel-deletion-error')).toHaveTextContent(refusal);
  });
});

describe('SettingsPage — engine', () => {
  beforeEach(() => vi.clearAllMocks());

  test('names a DocumentDB project as DocumentDB (MongoDB-compatible)', async () => {
    renderSettings(false, { documentDb: true });
    expect(await screen.findByText('DocumentDB (MongoDB-compatible)')).toBeInTheDocument();
  });

  test('names a plain project PostgreSQL', async () => {
    renderSettings(false);
    expect(await screen.findByText('PostgreSQL')).toBeInTheDocument();
  });
});

describe('SettingsPage — public database port', () => {
  beforeEach(() => vi.clearAllMocks());

  test('offers the public database port control next to the connection strings', async () => {
    renderSettings(false);
    expect(await screen.findByTestId('public-port-card')).toBeInTheDocument();
  });
});

describe('SettingsPage — private network between apps', () => {
  beforeEach(() => vi.clearAllMocks());

  const gets = (appHosting: boolean) => ({
    '/config': { deploymentMode: 'cloud', appHosting },
    '/projects/p-1/app-network': { projectId: 'p-1', privateNetwork: false, applied: false, canChange: true },
  });

  test('offers the setting when the installation hosts apps', async () => {
    renderSettings(false, {}, gets(true));
    expect(await screen.findByTestId('app-network-card')).toBeInTheDocument();
  });

  test('says nothing about app networking when apps are not hosted', async () => {
    renderSettings(false, {}, gets(false));
    expect(await screen.findByTestId('public-port-card')).toBeInTheDocument();
    expect(screen.queryByTestId('app-network-card')).not.toBeInTheDocument();
  });
});

describe('SettingsPage — after a deletion', () => {
  beforeEach(() => vi.clearAllMocks());

  // /projects is not a route: landing there left a blank page.
  test('a confirmed deletion lands on the projects list', async () => {
    renderSettings(false);
    vi.mocked(api.delete).mockResolvedValue({ data: { projectId: 'p-1', status: 'PENDING_DELETION' } } as never);
    await userEvent.click(await screen.findByTestId('delete-project-btn'));
    await userEvent.type(screen.getByTestId('confirm-input'), 'p-1');
    await userEvent.click(screen.getByTestId('modal-confirm'));

    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/provision/p-1', RESPOND_ASYNC));
    expect(await screen.findByTestId('projects-list-page')).toBeInTheDocument();
  });
});

describe('SettingsPage — lifecycle', () => {
  beforeEach(() => vi.clearAllMocks());

  test('an active project can be paused', async () => {
    renderSettings(false);
    await userEvent.click(await screen.findByTestId('pause-project-btn'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/provision/p-1/pause', { reason: 'manual' }, RESPOND_ASYNC),
    );
  });

  test('a paused project says so and can be resumed', async () => {
    renderSettings(false, { status: 'PAUSED', pauseReason: 'idle', lastActiveAt: '2026-09-30T10:00:00Z' });
    expect(await screen.findByTestId('lifecycle-section')).toHaveTextContent(/paused \(idle\)/);
    await userEvent.click(screen.getByTestId('resume-project-btn'));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/provision/p-1/resume', undefined, RESPOND_ASYNC));
  });

  test('copying the project ref puts it on the clipboard', async () => {
    renderSettings(false);
    const button = await screen.findByRole('button', { name: 'Copy Project ID' });
    const written: string[] = [];
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: (text: string) => { written.push(text); return Promise.resolve(); } },
    });
    await userEvent.click(button);
    expect(written).toEqual(['p-1']);
  });
});

// EXC-473: Studio does not hold a request open for a pause's backup or a
// deletion's stop; it follows the project and names a failure the server records.
describe('SettingsPage — operations followed on the project', () => {
  beforeEach(() => vi.clearAllMocks());

  test('a pause in progress says what it is doing', async () => {
    renderSettings(false);
    vi.mocked(api.post).mockReturnValue(new Promise(() => undefined) as never);
    await userEvent.click(await screen.findByTestId('pause-project-btn'));
    expect(await screen.findByTestId('lifecycle-pending')).toHaveTextContent(/backup/i);
  });

  test('a pause that did not complete names why', async () => {
    const reason = 'pause cancelled: the pre-pause backup did not complete; the project is still running';
    renderSettings(false, { failureReason: reason });
    vi.mocked(api.post).mockResolvedValue({ status: 202, data: { projectId: 'p-1', status: 'PAUSING' } } as never);
    await userEvent.click(await screen.findByTestId('pause-project-btn'));
    expect(await screen.findByTestId('lifecycle-error')).toHaveTextContent(/pre-pause backup did not complete/);
  });

  test('a deletion whose stop did not complete stays on the page and names why', async () => {
    const reason = 'pause did not complete: the project\'s database was not confirmed stopped; retry to continue';
    renderSettings(false, { failureReason: reason });
    vi.mocked(api.delete).mockResolvedValue({ status: 202, data: { projectId: 'p-1', status: 'PAUSING' } } as never);
    await userEvent.click(await screen.findByTestId('delete-project-btn'));
    await userEvent.type(screen.getByTestId('confirm-input'), 'p-1');
    await userEvent.click(screen.getByTestId('modal-confirm'));
    expect(await screen.findByTestId('delete-error')).toHaveTextContent(/not confirmed stopped/);
    expect(screen.queryByTestId('projects-list-page')).not.toBeInTheDocument();
  });
});

describe('SettingsPage — connect from code', () => {
  beforeEach(() => vi.clearAllMocks());

  test('the connect snippet uses the published SDK and the server-reported API URL', async () => {
    renderSettings(false, {}, { '/config': { deploymentMode: 'cloud', apiUrl: 'https://api.example.test' } });
    const code = await screen.findByTestId('connect-code');
    expect(code).toHaveTextContent("from '@excalibase/sdk'");
    expect(code).toHaveTextContent("url: 'https://api.example.test'");
    expect(code).toHaveTextContent("projectId: 'p-1'");
    expect(screen.getByTestId('connect-section')).not.toHaveTextContent(/@excalibase\/client|anonKey|api\.excalibase\.io\/o-1/);
  });
});

// EXC-555: a failed load spun forever and a refused toggle said nothing.
describe('SettingsPage — failures say why', () => {
  beforeEach(() => vi.clearAllMocks());

  const refusal = (status: number, error: string) => ({
    message: `Request failed with status code ${status}`,
    response: { status, data: { error, status } },
  });

  test('a project that cannot be loaded says so instead of spinning', async () => {
    vi.mocked(api.get).mockRejectedValue(refusal(404, 'project not found'));
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter initialEntries={['/project/p-1/settings']}>
          <Routes>
            <Route path="/project/:projectId/settings" element={<SettingsPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(await screen.findByTestId('settings-load-error')).toHaveTextContent('project not found');
  });

  test('a refused deletion-protection change shows the reason', async () => {
    renderSettings(false);
    vi.mocked(api.patch).mockRejectedValue(refusal(403, 'only org owners and admins can change deletion protection'));
    await userEvent.click(await screen.findByTestId('deletion-protection-btn'));
    expect(await screen.findByTestId('deletion-protection-error')).toHaveTextContent('only org owners and admins');
  });
});

// EXC-566: the browser-origin allowlist is set in Studio, not only over MCP.
describe('SettingsPage — allowed origins', () => {
  beforeEach(() => vi.clearAllMocks());

  test('shows the project origins, with or without a database', async () => {
    renderSettings(false, { noDatabase: true }, { '/projects/p-1/cors': { allowedOrigins: ['http://localhost:5173'], allowWildcard: false } });
    expect(await screen.findByTestId('cors-origins-card')).toHaveTextContent('Allowed origins');
    expect(await screen.findByText('http://localhost:5173')).toBeInTheDocument();
  });
});
