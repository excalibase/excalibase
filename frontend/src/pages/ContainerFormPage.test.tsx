import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { ContainerFormPage } from './ContainerFormPage';
import { api } from '../api/client';
import type { App } from '../api/apps';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), put: vi.fn() },
}));

const existing: App = {
  id: 'app-1',
  projectId: 'proj-1',
  name: 'web',
  image: 'nginx:1.27',
  env: [],
  port: 8080,
  replicas: 1,
  tier: 'STANDARD',
  status: 'PROVISIONING',
  version: 7,
  createdAt: '',
  updatedAt: '',
};

function renderAt(path: string) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({ data: { deploymentMode: 'cloud', appHosting: true } } as never);
    if (url === '/provision/proj-1') {
      return Promise.resolve({
        data: { projectId: 'proj-1', tier: 'STANDARD', databaseName: 'appdb' },
      } as never);
    }
    if (url === '/projects/proj-1/apps/app-1') return Promise.resolve({ data: existing } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/project/:projectId/containers/new" element={<ContainerFormPage />} />
          <Route
            path="/project/:projectId/containers/:appId/edit"
            element={<ContainerFormPage />}
          />
          <Route
            path="/project/:projectId/containers/:appId"
            element={<div data-testid="detail">detail</div>}
          />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return userEvent.setup();
}

describe('ContainerFormPage', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.post).mockReset();
    vi.mocked(api.patch).mockReset();
    vi.mocked(api.put).mockReset();
  });

  test('creates a container with the defaults and opens it', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { ...existing, id: 'app-9' } } as never);
    const user = renderAt('/project/proj-1/containers/new');
    await user.type(await screen.findByTestId('app-image'), 'nginx:1.27');
    await waitFor(() => expect(screen.getByTestId('app-size')).toHaveTextContent('Medium'));
    await user.click(screen.getByTestId('app-submit'));
    expect(await screen.findByTestId('detail')).toBeInTheDocument();
    expect(api.post).toHaveBeenCalledWith('/projects/proj-1/apps/', {
      name: 'nginx',
      image: 'nginx:1.27',
      port: 8080,
      replicas: 1,
      healthCheckPath: '',
      env: [],
    });
  });

  test('shows why the server refused the container', async () => {
    vi.mocked(api.post).mockRejectedValue({
      response: { status: 409, data: { error: 'project already has an app' } },
    });
    const user = renderAt('/project/proj-1/containers/new');
    await user.type(await screen.findByTestId('app-image'), 'nginx:1.27');
    await user.click(screen.getByTestId('app-submit'));
    expect(await screen.findByRole('alert')).toHaveTextContent('project already has an app');
    expect(screen.queryByTestId('detail')).not.toBeInTheDocument();
  });

  test('saves an edit against the version it was read at', async () => {
    vi.mocked(api.patch).mockResolvedValue({ data: { ...existing, version: 8 } } as never);
    const user = renderAt('/project/proj-1/containers/app-1/edit');
    const port = await screen.findByTestId('app-port');
    await user.clear(port);
    await user.type(port, '3000');
    await user.click(screen.getByTestId('app-submit'));
    expect(await screen.findByTestId('detail')).toBeInTheDocument();
    expect(api.patch).toHaveBeenCalledWith(
      '/projects/proj-1/apps/app-1',
      expect.objectContaining({ port: 3000, image: 'nginx:1.27' }),
      { headers: { 'If-Match': '7' } },
    );
  });

  test('stores a new secret through the write-only endpoint after creating the container', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { ...existing, id: 'app-9' } } as never);
    vi.mocked(api.put).mockResolvedValue({ data: { name: 'API_KEY', set: true } } as never);
    const user = renderAt('/project/proj-1/containers/new');
    await user.type(await screen.findByTestId('app-image'), 'nginx:1.27');
    await user.click(screen.getByTestId('env-add'));
    await user.type(screen.getByTestId('env-name-0'), 'API_KEY');
    await user.selectOptions(screen.getByTestId('env-kind-0'), 'secret');
    await user.type(screen.getByTestId('env-secret-value-0'), 'sk_live_123');
    await user.click(screen.getByTestId('app-submit'));

    expect(await screen.findByTestId('detail')).toBeInTheDocument();
    expect(api.post).toHaveBeenCalledWith(
      '/projects/proj-1/apps/',
      expect.objectContaining({ env: [] }),
    );
    expect(api.put).toHaveBeenCalledWith('/projects/proj-1/apps/app-9/secrets/API_KEY', {
      value: 'sk_live_123',
    });
  });

  test('replaces a stored secret after saving the edit', async () => {
    vi.mocked(api.patch).mockResolvedValue({ data: { ...existing, version: 8 } } as never);
    vi.mocked(api.put).mockResolvedValue({ data: { name: 'API_KEY', set: true } } as never);
    existing.env = [
      {
        name: 'API_KEY',
        kind: 'secret',
        secret: { path: 'projects/proj-1/apps/app-1/env/API_KEY', key: 'value' },
      },
    ];
    try {
      const user = renderAt('/project/proj-1/containers/app-1/edit');
      await user.click(await screen.findByTestId('env-secret-replace-0'));
      await user.type(screen.getByTestId('env-secret-value-0'), 'rotated');
      await user.click(screen.getByTestId('app-submit'));
      expect(await screen.findByTestId('detail')).toBeInTheDocument();
      expect(api.patch).toHaveBeenCalledWith(
        '/projects/proj-1/apps/app-1',
        expect.objectContaining({ env: existing.env }),
        { headers: { 'If-Match': '7' } },
      );
      expect(api.put).toHaveBeenCalledWith('/projects/proj-1/apps/app-1/secrets/API_KEY', {
        value: 'rotated',
      });
    } finally {
      existing.env = [];
    }
  });

  test('says which secret was not stored when the app saved but the secret did not', async () => {
    vi.mocked(api.patch).mockResolvedValue({ data: { ...existing, version: 8 } } as never);
    vi.mocked(api.put).mockRejectedValue({
      response: { status: 503, data: { error: 'secrets are unavailable: no vault is configured' } },
    });
    const user = renderAt('/project/proj-1/containers/app-1/edit');
    await user.click(await screen.findByTestId('env-add'));
    await user.type(screen.getByTestId('env-name-0'), 'API_KEY');
    await user.selectOptions(screen.getByTestId('env-kind-0'), 'secret');
    await user.type(screen.getByTestId('env-secret-value-0'), 'sk_live_123');
    await user.click(screen.getByTestId('app-submit'));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(/saved, but the secret API_KEY was not/i);
    expect(alert).toHaveTextContent('no vault is configured');
    expect(alert).not.toHaveTextContent('sk_live_123');
    expect(screen.queryByTestId('detail')).not.toBeInTheDocument();
  });

  test('moves to the edit page when the container was created but a secret was not stored', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: existing } as never);
    vi.mocked(api.put).mockRejectedValue({
      response: { status: 500, data: { error: 'could not store the secret' } },
    });
    const user = renderAt('/project/proj-1/containers/new');
    await user.type(await screen.findByTestId('app-image'), 'nginx:1.27');
    await user.click(screen.getByTestId('env-add'));
    await user.type(screen.getByTestId('env-name-0'), 'API_KEY');
    await user.selectOptions(screen.getByTestId('env-kind-0'), 'secret');
    await user.type(screen.getByTestId('env-secret-value-0'), 'sk_live_123');
    await user.click(screen.getByTestId('app-submit'));
    expect(await screen.findByText('Edit web')).toBeInTheDocument();
    expect(api.post).toHaveBeenCalledTimes(1);
  });
});
