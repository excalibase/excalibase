import { describe, test, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AutoDeployCard } from './AutoDeployCard';
import { api } from '../../api/client';
import type { App } from '../../api/apps';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), patch: vi.fn() } }));

const conflict = {
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: 'the container changed since you loaded it', status: 409 } },
};

const app: App = {
  id: 'app-1',
  projectId: 'proj-1',
  name: 'web',
  image: 'ghcr.io/acme/web:main',
  env: [],
  port: 8080,
  replicas: 1,
  tier: 'STANDARD',
  status: 'ACTIVE',
  version: 4,
  createdAt: '2026-09-20T10:00:00Z',
  updatedAt: '2026-09-20T10:00:00Z',
};

function mount(overrides: Partial<App> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <AutoDeployCard app={{ ...app, ...overrides }} />
    </QueryClientProvider>,
  );
}

describe('AutoDeployCard', () => {
  beforeEach(() => vi.clearAllMocks());

  test('turning auto-deploy on sends the version the page loaded', async () => {
    vi.mocked(api.patch).mockResolvedValue({ data: { ...app, autoDeploy: true } } as never);
    mount();
    fireEvent.click(screen.getByTestId('auto-deploy-toggle'));
    await vi.waitFor(() =>
      expect(api.patch).toHaveBeenCalledWith(
        '/projects/proj-1/apps/app-1',
        { autoDeploy: true },
        { headers: { 'If-Match': '4' } },
      ),
    );
  });

  test('an image pinned by digest cannot be watched and says how to fix it', () => {
    mount({ image: 'ghcr.io/acme/web@sha256:abc' });
    expect(screen.getByTestId('auto-deploy-toggle')).toBeDisabled();
    expect(screen.getByTestId('auto-deploy-pinned')).toHaveTextContent('ghcr.io/acme/web:main');
  });

  test('shows what the watch last saw and why its last check failed', () => {
    mount({
      autoDeploy: true,
      imageWatch: { digest: 'sha256:0123456789abcdef', checkedAt: '2026-09-28T10:00:00Z', error: 'registry said 401' },
    });
    expect(screen.getByTestId('image-watch')).toHaveTextContent('ghcr.io/acme/web:main');
    expect(screen.getByTestId('image-watch-error')).toHaveTextContent('registry said 401');
  });

  test('a fresh watch says when the first check runs', () => {
    mount({ autoDeploy: true });
    expect(screen.getByTestId('image-watch')).toHaveTextContent(/first check/i);
  });

  test('a refusal shows the server reason, not the status code', async () => {
    vi.mocked(api.patch).mockRejectedValue(conflict);
    mount();
    fireEvent.click(screen.getByTestId('auto-deploy-toggle'));
    expect(await screen.findByRole('alert')).toHaveTextContent('the container changed since you loaded it');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
    expect(screen.getByTestId('auto-deploy-toggle')).not.toBeChecked();
  });

  test('two quick clicks send one change', async () => {
    vi.mocked(api.patch).mockReturnValue(new Promise(() => {}));
    mount();
    fireEvent.click(screen.getByTestId('auto-deploy-toggle'));
    fireEvent.click(screen.getByTestId('auto-deploy-toggle'));
    await vi.waitFor(() => expect(api.patch).toHaveBeenCalled());
    expect(api.patch).toHaveBeenCalledTimes(1);
  });
});
