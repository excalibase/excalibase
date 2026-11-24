import { describe, test, expect, vi, beforeEach } from 'vitest';
import { act, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ProjectVariablesPage } from './ProjectVariablesPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), patch: vi.fn(), delete: vi.fn() } }));

const answers: Promise<unknown>[] = [];

// Waits for every answer the mocked API has given and for the screen to take them in, so an
// assertion that something is absent cannot pass just because nothing has loaded yet.
async function settle() {
  await act(async () => {
    await Promise.allSettled(answers);
    await new Promise((resolve) => setTimeout(resolve, 20));
  });
}

const app = (over: Record<string, unknown>) => ({
  id: 'a1', projectId: 'p1', name: 'api', image: 'x', port: 80, replicas: 1, tier: 'FREE', status: 'ACTIVE', version: 1, createdAt: '', updatedAt: '', env: [], ...over,
});

const API_APP = app({
  env: [
    { name: 'API_BASE_URL', kind: 'literal', value: 'https://api.example.test' },
    { name: 'STRIPE_SECRET', kind: 'secret', secret: { path: 'p1/api', key: 'STRIPE_SECRET' } },
    { name: 'LEGACY_TOKEN', kind: 'literal', value: 'plain-but-credential-shaped' },
    { name: 'DATABASE_URL', kind: 'reference', reference: { sourceKind: 'database', sourceName: 'main', variable: 'DATABASE_URL' } },
  ],
});
const WORKER = app({ id: 'a2', name: 'worker', env: [] });

function renderPage(apps: unknown[] | Error = [API_APP, WORKER]) {
  answers.length = 0;
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url !== '/projects/p1/apps/') return Promise.reject(new Error(`unexpected GET ${url}`));
    const answer = apps instanceof Error ? Promise.reject(apps) : Promise.resolve({ data: apps });
    answers.push(answer);
    return answer as never;
  });
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={['/project/p1/variables']}>
        <Routes><Route path="/project/:projectId/variables" element={<ProjectVariablesPage />} /></Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const rowOf = (name: string) => screen.getByTestId(`variable-${name}`);

beforeEach(() => {
  vi.clearAllMocks();
  Object.assign(navigator, { clipboard: { writeText: vi.fn().mockResolvedValue(undefined) } });
});

describe('ProjectVariablesPage', () => {
  test('lists the variables of each service', async () => {
    renderPage();
    expect(await screen.findByRole('heading', { name: 'Variables' })).toBeInTheDocument();
    expect(await screen.findByRole('heading', { name: 'api' })).toBeInTheDocument();
    expect(rowOf('API_BASE_URL')).toHaveTextContent('https://api.example.test');
    expect(rowOf('DATABASE_URL')).toHaveTextContent('database main');
  });

  test('a service with no variables is left out', async () => {
    renderPage();
    await screen.findByRole('heading', { name: 'api' });
    await settle();
    expect(screen.queryByRole('heading', { name: 'worker' })).not.toBeInTheDocument();
  });

  test('a secret stays masked and offers neither reveal nor copy', async () => {
    renderPage();
    await screen.findByTestId('variable-STRIPE_SECRET');
    await settle();
    const row = rowOf('STRIPE_SECRET');
    expect(row).toHaveTextContent('Set');
    expect(within(row).queryByRole('button')).not.toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/p1\/api/);
  });

  test('a credential-shaped plain value is masked until Reveal is clicked', async () => {
    renderPage();
    await screen.findByTestId('variable-LEGACY_TOKEN');
    await settle();
    expect(rowOf('LEGACY_TOKEN')).not.toHaveTextContent('plain-but-credential-shaped');
    await userEvent.click(within(rowOf('LEGACY_TOKEN')).getByRole('button', { name: /reveal legacy_token/i }));
    expect(rowOf('LEGACY_TOKEN')).toHaveTextContent('plain-but-credential-shaped');
    await userEvent.click(within(rowOf('LEGACY_TOKEN')).getByRole('button', { name: /hide legacy_token/i }));
    expect(rowOf('LEGACY_TOKEN')).not.toHaveTextContent('plain-but-credential-shaped');
  });

  test('nothing is copied until Copy is clicked, and then only that value', async () => {
    renderPage();
    await screen.findByTestId('variable-API_BASE_URL');
    await settle();
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled();
    await userEvent.click(within(rowOf('API_BASE_URL')).getByRole('button', { name: /copy api_base_url/i }));
    expect(navigator.clipboard.writeText).toHaveBeenCalledExactlyOnceWith('https://api.example.test');
    expect(await within(rowOf('API_BASE_URL')).findByText('Copied')).toBeInTheDocument();
  });

  test('a masked value is not copyable before it is revealed', async () => {
    renderPage();
    await screen.findByTestId('variable-LEGACY_TOKEN');
    expect(within(rowOf('LEGACY_TOKEN')).queryByRole('button', { name: /^copy/i })).not.toBeInTheDocument();
  });

  test('a role that may not read plain values sees the names and a notice', async () => {
    renderPage([app({ env: [{ name: 'API_BASE_URL', kind: 'literal' }] })]);
    await screen.findByTestId('variable-API_BASE_URL');
    await settle();
    const row = rowOf('API_BASE_URL');
    expect(row).toHaveTextContent('Hidden for your role');
    expect(within(row).queryByRole('button')).not.toBeInTheDocument();
  });

  test('each service links to its own variables editor', async () => {
    renderPage();
    const link = await screen.findByRole('link', { name: /edit variables of api/i });
    expect(link).toHaveAttribute('href', '/project/p1?service=app%3Aa1&tab=variables');
  });

  test('values never reach the address bar or the console', async () => {
    const log = vi.spyOn(console, 'log');
    const error = vi.spyOn(console, 'error');
    renderPage();
    await screen.findByTestId('variable-LEGACY_TOKEN');
    await userEvent.click(within(rowOf('LEGACY_TOKEN')).getByRole('button', { name: /reveal/i }));
    expect(globalThis.location.href).not.toContain('plain-but-credential-shaped');
    for (const spy of [log, error]) expect(JSON.stringify(spy.mock.calls)).not.toContain('plain-but-credential-shaped');
  });

  test('a project with no services says so', async () => {
    renderPage([]);
    expect(await screen.findByText(/no services with variables yet/i)).toBeInTheDocument();
  });

  test('a failed load says so', async () => {
    renderPage(new Error('down'));
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not load/i);
  });
});
