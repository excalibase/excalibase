import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { RegistryCredentials } from './RegistryCredentials';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn(), delete: vi.fn() },
}));

const TOKEN = 'ghp_write_only_token';

function renderSection(registries: string[]) {
  const state = { registries: [...registries] };
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/projects/proj-1/registry-credentials/')
      return Promise.resolve({ data: state.registries.map((registry) => ({ registry })) } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  vi.mocked(api.put).mockImplementation((url: string) => {
    const registry = decodeURIComponent(url.split('/').pop() ?? '');
    state.registries = [...new Set([...state.registries, registry])];
    return Promise.resolve({ data: { registry, set: true } } as never);
  });
  vi.mocked(api.delete).mockImplementation((url: string) => {
    const registry = decodeURIComponent(url.split('/').pop() ?? '');
    state.registries = state.registries.filter((r) => r !== registry);
    return Promise.resolve({ status: 204 } as never);
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <RegistryCredentials projectId="proj-1" />
    </QueryClientProvider>,
  );
  return userEvent.setup();
}

describe('RegistryCredentials', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.put).mockReset();
    vi.mocked(api.delete).mockReset();
  });

  test('lists the registries that have a credential and nothing else', async () => {
    renderSection(['ghcr.io']);
    expect(await screen.findByTestId('registry-row-ghcr.io')).toHaveTextContent('ghcr.io');
  });

  test('saving sends the credential once and forgets it', async () => {
    const user = renderSection([]);
    await user.type(await screen.findByTestId('registry-host'), 'ghcr.io');
    await user.type(screen.getByTestId('registry-username'), 'octocat');
    await user.type(screen.getByTestId('registry-password'), TOKEN);
    expect(screen.getByTestId('registry-password')).toHaveAttribute('type', 'password');
    await user.click(screen.getByTestId('registry-save'));

    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/projects/proj-1/registry-credentials/ghcr.io', {
        username: 'octocat',
        password: TOKEN,
      }),
    );
    expect(await screen.findByTestId('registry-row-ghcr.io')).toBeInTheDocument();
    expect(screen.getByTestId('registry-password')).toHaveValue('');
    expect(screen.queryByDisplayValue(TOKEN)).not.toBeInTheDocument();
  });

  test('removing asks first', async () => {
    const user = renderSection(['ghcr.io']);
    const row = await screen.findByTestId('registry-row-ghcr.io');
    await user.click(within(row).getByTestId('registry-remove-ghcr.io'));
    expect(api.delete).not.toHaveBeenCalled();
    await user.click(within(row).getByTestId('registry-remove-confirm-ghcr.io'));
    await waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith('/projects/proj-1/registry-credentials/ghcr.io'),
    );
    await waitFor(() =>
      expect(screen.queryByTestId('registry-row-ghcr.io')).not.toBeInTheDocument(),
    );
  });

  test('a refusal is shown', async () => {
    const user = renderSection([]);
    vi.mocked(api.put).mockRejectedValueOnce({
      response: {
        data: { error: 'invalid registry credential: the username must not contain a colon' },
      },
    });
    await user.type(await screen.findByTestId('registry-host'), 'ghcr.io');
    await user.type(screen.getByTestId('registry-username'), 'a:b');
    await user.type(screen.getByTestId('registry-password'), 'x');
    await user.click(screen.getByTestId('registry-save'));
    expect(await screen.findByRole('alert')).toHaveTextContent(/must not contain a colon/);
  });
});
