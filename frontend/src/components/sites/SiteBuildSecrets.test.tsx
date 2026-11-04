import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { SiteBuildSecrets } from './SiteBuildSecrets';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn() },
}));

const SECRETS_URL = '/projects/proj-1/sites/build-secrets';

function renderSecrets(secrets: { name: string; buildVisible: boolean }[], appGitBuilds = false) {
  vi.mocked(api.get).mockImplementation((url: string) =>
    Promise.resolve({
      data:
        url === '/config'
          ? { deploymentMode: 'cloud', features: { appgitbuilds: appGitBuilds } }
          : secrets,
    } as never),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SiteBuildSecrets projectId="proj-1" />
    </QueryClientProvider>,
  );
}

beforeEach(() => vi.resetAllMocks());

describe('SiteBuildSecrets', () => {
  test('secrets start hidden from builds', async () => {
    renderSecrets([{ name: 'DB_PASSWORD', buildVisible: false }]);
    expect(await screen.findByTestId('build-secret-DB_PASSWORD')).not.toBeChecked();
  });

  test('unmarking a secret sends the rest', async () => {
    renderSecrets([
      { name: 'A_KEY', buildVisible: true },
      { name: 'B_KEY', buildVisible: true },
    ]);
    vi.mocked(api.put).mockResolvedValue({ data: [] } as never);
    await userEvent.click(await screen.findByTestId('build-secret-A_KEY'));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(SECRETS_URL, { buildVisible: ['B_KEY'] }),
    );
  });

  test('with app git builds on, it says every step of an app build can read a marked secret', async () => {
    renderSecrets([{ name: 'NPM_TOKEN', buildVisible: true }], true);
    expect(await screen.findByTestId('app-build-secrets-warning')).toHaveTextContent(
      /any step of an app build/i,
    );
  });

  test('with app git builds off, only the site warning shows', async () => {
    renderSecrets([{ name: 'NPM_TOKEN', buildVisible: true }]);
    expect(await screen.findByTestId('site-build-secrets-warning')).toBeInTheDocument();
    expect(screen.queryByTestId('app-build-secrets-warning')).not.toBeInTheDocument();
  });

  test('a project without secrets says so', async () => {
    renderSecrets([]);
    expect(await screen.findByText('This project has no secrets.')).toBeInTheDocument();
  });
});
