import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { SiteServerEnv } from './SiteServerEnv';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn() },
}));

const ENV_URL = '/projects/proj-1/sites/site-1/env';
const SECRETS_URL = '/projects/proj-1/sites/server-secrets';

function renderEnv(
  vars: Record<string, string>,
  secrets: { name: string; serverVisible: boolean }[] = [],
) {
  vi.mocked(api.get).mockImplementation((url: string) =>
    Promise.resolve({ data: url === ENV_URL ? { vars } : secrets } as never),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SiteServerEnv projectId="proj-1" siteId="site-1" />
    </QueryClientProvider>,
  );
}

beforeEach(() => vi.resetAllMocks());

describe('SiteServerEnv', () => {
  test('shows the variables and says what the platform sets itself', async () => {
    renderEnv({ MODE: 'a' });
    expect(await screen.findByTestId('env-value-MODE')).toHaveValue('a');
    expect(screen.getByTestId('env-platform-note')).toHaveTextContent(/EXCALIBASE_URL/);
    expect(screen.getByTestId('env-platform-note')).toHaveTextContent(/EXCALIBASE_PUBLISHABLE_KEY/);
  });

  test('adding a variable saves the whole set and says it applies on the next deploy', async () => {
    renderEnv({ MODE: 'a' });
    vi.mocked(api.put).mockResolvedValue({ data: { vars: {} } } as never);
    await userEvent.type(await screen.findByTestId('env-new-name'), 'FEATURE_X');
    await userEvent.type(screen.getByTestId('env-new-value'), 'on');
    await userEvent.click(screen.getByTestId('env-add'));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith(ENV_URL, { vars: { MODE: 'a', FEATURE_X: 'on' } }),
    );
    expect(await screen.findByTestId('env-saved')).toHaveTextContent(/next deploy/i);
  });

  test('removing a variable sends the rest', async () => {
    renderEnv({ A: '1', B: '2' });
    vi.mocked(api.put).mockResolvedValue({ data: { vars: {} } } as never);
    await userEvent.click(await screen.findByTestId('env-remove-A'));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(ENV_URL, { vars: { B: '2' } }));
  });

  test('a refused name shows the server reason', async () => {
    renderEnv({});
    vi.mocked(api.put).mockRejectedValue({
      response: { data: { error: 'EXCALIBASE_URL is reserved by the platform' } },
    });
    await userEvent.type(await screen.findByTestId('env-new-name'), 'EXCALIBASE_URL');
    await userEvent.click(screen.getByTestId('env-add'));
    expect(await screen.findByRole('alert')).toHaveTextContent(/reserved by the platform/);
  });

  test('secrets are marked server-visible with a warning, and unmarking sends the rest', async () => {
    renderEnv({}, [
      { name: 'STRIPE_KEY', serverVisible: true },
      { name: 'DB_PASSWORD', serverVisible: false },
    ]);
    vi.mocked(api.put).mockResolvedValue({ data: [] } as never);
    expect(await screen.findByTestId('server-secrets-warning')).toHaveTextContent(
      /runs in that Worker/i,
    );
    expect(screen.getByTestId('server-secret-DB_PASSWORD')).not.toBeChecked();
    await userEvent.click(screen.getByTestId('server-secret-STRIPE_KEY'));
    await waitFor(() => expect(api.put).toHaveBeenCalledWith(SECRETS_URL, { serverVisible: [] }));
  });
});
