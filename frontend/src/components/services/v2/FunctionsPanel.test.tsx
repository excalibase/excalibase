import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { api } from '../../../api/client';
import { useAuthStore } from '../../../stores/auth-store';
import { FunctionsPanel } from './FunctionsPanel';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

const fn = (id: string, extra: Record<string, unknown> = {}) => ({
  id, projectId: 'p1', name: `Fn ${id}`, files: [{ path: 'index.ts', content: 'x' }], active: true, version: 3,
  createdAt: '2026-10-01T00:00:00Z', updatedAt: '2026-10-02T00:00:00Z', ...extra,
});

interface World {
  functions?: unknown[];
  secrets?: unknown[];
  logs?: unknown[];
  runtime?: unknown;
  apiUrl?: string;
  role?: string;
}
let world: World = {};

function route(url: string) {
  const ok = (data: unknown) => Promise.resolve({ data });
  if (url === '/orgs') return ok([{ id: 'org-1', name: 'Acme', role: world.role ?? 'owner' }]);
  if (url === '/config') return ok({ deploymentMode: 'cloud', features: { studio_v2: true }, ...(world.apiUrl ? { apiUrl: world.apiUrl } : {}) });
  if (url === '/provision/p1') return ok({ projectId: 'p1', orgId: 'org-1' });
  if (url.endsWith('/functions')) return ok(world.functions ?? []);
  if (url.endsWith('/functions/runtime/status')) return ok(world.runtime ?? { status: 'healthy', healthy: true });
  if (url.endsWith('/functions/secrets')) return ok(world.secrets ?? []);
  if (url.endsWith('/logs')) return ok({ logs: world.logs ?? [] });
  if (url.endsWith('/functions/egress') || url.includes('egress')) return ok({ hosts: [] });
  return Promise.reject(Object.assign(new Error('not mocked'), { response: { status: 404, data: {} } }));
}

function renderPanel(tab: string) {
  const onTab = vi.fn();
  const onClose = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <FunctionsPanel projectId="p1" tab={tab} onTab={onTab} onClose={onClose} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return { onTab, onClose };
}

beforeEach(() => {
  vi.clearAllMocks();
  world = { functions: [fn('hello'), fn('bye', { active: false, version: 1 })], apiUrl: 'https://api.example.com/' };
  useAuthStore.setState({ user: { id: '1', username: 'u', email: 'e', role: 'user' } } as never);
  vi.mocked(api.get).mockImplementation(route as never);
  vi.mocked(api.post).mockResolvedValue({ data: '{"ok":true}' } as never);
  vi.mocked(api.delete).mockResolvedValue({ data: {} } as never);
});

describe('functions panel: functions tab', () => {
  test('lists each function with its deploy status and the runtime state', async () => {
    renderPanel('functions');
    const hello = await screen.findByRole('listitem', { name: 'Fn hello' });
    expect(within(hello).getByText('v3')).toBeInTheDocument();
    expect(within(hello).getByText('Active')).toBeInTheDocument();
    expect(within(screen.getByRole('listitem', { name: 'Fn bye' })).getByText('Inactive')).toBeInTheDocument();
    expect(await screen.findByTestId('panel-status')).toHaveTextContent('Running');
  });

  test('shows the public invoke URL built from the API address', async () => {
    renderPanel('functions');
    const hello = await screen.findByRole('listitem', { name: 'Fn hello' });
    expect(await within(hello).findByText('https://api.example.com/functions/v1/p1/hello')).toBeInTheDocument();
  });

  test('says so when the install reports no public address, instead of guessing one', async () => {
    world.apiUrl = undefined;
    renderPanel('functions');
    const hello = await screen.findByRole('listitem', { name: 'Fn hello' });
    expect(await within(hello).findByText(/does not report its public API address/i)).toBeInTheDocument();
    expect(within(hello).queryByText(/functions\/v1/)).not.toBeInTheDocument();
  });

  test('invoking posts to the project route and prints the answer', async () => {
    renderPanel('functions');
    const hello = await screen.findByRole('listitem', { name: 'Fn hello' });
    await userEvent.click(await within(hello).findByRole('button', { name: 'Invoke' }));
    expect(api.post).toHaveBeenCalledWith('/projects/p1/functions/hello/invoke', expect.any(String), expect.anything());
    expect(await within(hello).findByTestId('invoke-result')).toHaveTextContent('{"ok":true}');
  });

  test('a viewer can read the list but cannot invoke or delete', async () => {
    world.role = 'viewer';
    renderPanel('functions');
    const hello = await screen.findByRole('listitem', { name: 'Fn hello' });
    expect(await within(hello).findByText('https://api.example.com/functions/v1/p1/hello')).toBeInTheDocument();
    expect(within(hello).queryByRole('button', { name: 'Invoke' })).not.toBeInTheDocument();
    expect(within(hello).queryByRole('button', { name: 'Delete' })).not.toBeInTheDocument();
  });

  test('delete asks for the function id and sends nothing until it is typed', async () => {
    renderPanel('functions');
    const hello = await screen.findByRole('listitem', { name: 'Fn hello' });
    await userEvent.click(await within(hello).findByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    const confirm = within(dialog).getByRole('button', { name: 'Delete' });
    expect(confirm).toBeDisabled();
    expect(api.delete).not.toHaveBeenCalled();
    await userEvent.type(within(dialog).getByRole('textbox'), 'hello');
    await userEvent.click(confirm);
    expect(api.delete).toHaveBeenCalledWith('/projects/p1/functions/hello');
  });

  test('a delete the server refuses is shown, not swallowed', async () => {
    vi.mocked(api.delete).mockRejectedValue(Object.assign(new Error('x'), { response: { status: 409, data: { error: 'in use by a schedule' } } }));
    renderPanel('functions');
    const hello = await screen.findByRole('listitem', { name: 'Fn hello' });
    await userEvent.click(await within(hello).findByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    await userEvent.type(within(dialog).getByRole('textbox'), 'hello');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/in use by a schedule/);
  });
});

describe('functions panel: access', () => {
  test('a viewer gets only the Functions tab, even from a link to Secrets or Logs', async () => {
    world.role = 'viewer';
    renderPanel('secrets');
    expect(await screen.findByRole('listitem', { name: 'Fn hello' })).toBeInTheDocument();
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Functions']);
    expect(screen.queryByTestId('function-secrets')).not.toBeInTheDocument();
    expect(vi.mocked(api.get).mock.calls.some(([url]) => String(url).endsWith('/secrets') || String(url).endsWith('/logs'))).toBe(false);
  });

  test('a developer gets all three tabs', async () => {
    renderPanel('functions');
    expect(await screen.findByRole('tab', { name: 'Secrets' })).toBeInTheDocument();
    expect(screen.getAllByRole('tab').map((t) => t.textContent)).toEqual(['Functions', 'Logs', 'Secrets']);
  });

  test('with no functions it says so and links to the page that deploys one', async () => {
    world.functions = [];
    renderPanel('functions');
    expect(await screen.findByText('No functions in this project.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Deploy function' })).toHaveAttribute('href', '/project/p1/edge-functions');
  });
});

describe('functions panel: logs tab', () => {
  test('reads the chosen function logs, newest lines as the runtime sends them', async () => {
    world.logs = [{ level: 'error', msg: 'boom happened', ts: 1760000000000 }, { level: 'info', msg: 'started', ts: 1760000001000 }];
    renderPanel('logs');
    expect(await screen.findByText('boom happened')).toBeInTheDocument();
    expect(screen.getByText('started')).toBeInTheDocument();
    expect(vi.mocked(api.get).mock.calls.some(([url]) => url === '/projects/p1/functions/hello/logs')).toBe(true);
  });

  test('switching the function reads that function', async () => {
    renderPanel('logs');
    await userEvent.selectOptions(await screen.findByRole('combobox', { name: 'Function' }), 'bye');
    expect(await screen.findByText(/No logs yet/)).toBeInTheDocument();
    expect(vi.mocked(api.get).mock.calls.some(([url]) => url === '/projects/p1/functions/bye/logs')).toBe(true);
  });
});

describe('functions panel: secrets tab', () => {
  test('lists key names only', async () => {
    world.secrets = [{ key: 'STRIPE_KEY' }];
    renderPanel('secrets');
    expect(await screen.findByText('STRIPE_KEY')).toBeInTheDocument();
  });

  test('saves a secret under an upper-cased key', async () => {
    renderPanel('secrets');
    await userEvent.type(await screen.findByLabelText('Key'), 'api_base');
    await userEvent.type(screen.getByLabelText('Value'), 'https://x');
    await userEvent.click(screen.getByRole('button', { name: 'Save secret' }));
    expect(api.post).toHaveBeenCalledWith('/projects/p1/functions/secrets', { key: 'API_BASE', value: 'https://x' });
  });

  test('deleting a secret asks first', async () => {
    world.secrets = [{ key: 'STRIPE_KEY' }];
    renderPanel('secrets');
    await userEvent.click(await screen.findByRole('button', { name: 'Delete STRIPE_KEY' }));
    expect(api.delete).not.toHaveBeenCalled();
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Delete' }));
    expect(api.delete).toHaveBeenCalledWith('/projects/p1/functions/secrets/STRIPE_KEY');
  });
});
