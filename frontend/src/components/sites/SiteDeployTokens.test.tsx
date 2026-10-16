import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { SiteDeployTokens } from './SiteDeployTokens';
import { api } from '../../api/client';
import type { DeployToken } from '../../api/sites';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

const TOKENS_URL = '/projects/proj-1/sites/site-1/tokens';

const tokenFixture = (overrides: Partial<DeployToken> = {}): DeployToken => ({
  id: 'tok-1',
  siteId: 'site-1',
  projectId: 'proj-1',
  name: 'github actions',
  tokenPrefix: 'excd_1a2b3c4',
  createdBy: 'u1',
  createdAt: '2026-10-15T10:00:00Z',
  expiresAt: '2027-01-13T10:00:00Z',
  creatorCanDeploy: true,
  ...overrides,
});

function renderTokens(tokens: DeployToken[] = [tokenFixture()]) {
  vi.mocked(api.get).mockImplementation((url: string) =>
    url === TOKENS_URL
      ? Promise.resolve({ data: tokens } as never)
      : Promise.reject(new Error(`unexpected GET ${url}`)),
  );
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SiteDeployTokens projectId="proj-1" siteId="site-1" />
    </QueryClientProvider>,
  );
}

// EXC-620: a site's CI credential, made by a Developer and shown once.
describe('SiteDeployTokens', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.post).mockReset();
    vi.mocked(api.delete).mockReset();
  });

  test('lists each token by name and prefix, never the token itself', async () => {
    renderTokens([
      tokenFixture(),
      tokenFixture({ id: 'tok-2', name: 'gitlab', lastUsedAt: '2026-10-16T10:00:00Z' }),
    ]);
    const row = await screen.findByTestId('site-deploy-token-tok-1');
    expect(row).toHaveTextContent('github actions');
    expect(row).toHaveTextContent('excd_1a2b3c4…');
    expect(row).toHaveTextContent('Never used');
    expect(screen.getByTestId('site-deploy-token-tok-2')).toHaveTextContent('Last used');
  });

  test('says how to make one when the site has none', async () => {
    renderTokens([]);
    expect(await screen.findByTestId('site-deploy-tokens-empty')).toHaveTextContent(
      /no deploy tokens/i,
    );
  });

  test('creates a token and shows it once, to copy into CI', async () => {
    const user = userEvent.setup();
    renderTokens([]);
    vi.mocked(api.post).mockResolvedValue({
      data: { ...tokenFixture({ id: 'tok-9', name: 'ci' }), token: 'excd_the-secret' },
    } as never);
    await user.type(await screen.findByTestId('site-deploy-token-name'), 'ci');
    await user.click(screen.getByTestId('site-deploy-token-create'));
    expect(api.post).toHaveBeenCalledWith(TOKENS_URL, { name: 'ci', expiresInDays: 90 });
    const issued = await screen.findByTestId('site-deploy-token-issued');
    expect(issued).toHaveTextContent('excd_the-secret');
    expect(issued).toHaveTextContent('EXCALIBASE_DEPLOY_TOKEN');
    expect(issued).toHaveTextContent(/shown once/i);
    await user.click(within(issued).getByRole('button', { name: /done/i }));
    expect(screen.queryByText('excd_the-secret')).not.toBeInTheDocument();
  });

  test('needs a name before it creates', async () => {
    renderTokens([]);
    expect(await screen.findByTestId('site-deploy-token-create')).toBeDisabled();
  });

  test('shows why a create was refused', async () => {
    const user = userEvent.setup();
    renderTokens([]);
    vi.mocked(api.post).mockRejectedValue(
      Object.assign(new Error('x'), {
        response: {
          status: 409,
          data: { error: 'the site already holds as many deploy tokens as it may' },
        },
      }),
    );
    await user.type(await screen.findByTestId('site-deploy-token-name'), 'ci');
    await user.click(screen.getByTestId('site-deploy-token-create'));
    expect(await screen.findByRole('alert')).toHaveTextContent('as many deploy tokens');
  });

  test('revokes a token after asking, at once', async () => {
    const user = userEvent.setup();
    renderTokens();
    vi.mocked(api.delete).mockResolvedValue({} as never);
    await user.click(await screen.findByTestId('site-deploy-token-revoke-tok-1'));
    expect(api.delete).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: 'Revoke token' }));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith(`${TOKENS_URL}/tok-1`));
  });

  test('shows when each token expires, and an expired one as expired', async () => {
    renderTokens([
      tokenFixture(),
      tokenFixture({ id: 'tok-old', name: 'old ci', expiresAt: '2020-01-01T00:00:00Z' }),
    ]);
    expect(await screen.findByTestId('site-deploy-token-tok-1')).toHaveTextContent(/expires/i);
    expect(screen.getByTestId('site-deploy-token-tok-old')).toHaveTextContent(/expired/i);
  });

  test('marks a token inactive when its creator can no longer deploy', async () => {
    renderTokens([tokenFixture({ creatorCanDeploy: false })]);
    expect(await screen.findByTestId('site-deploy-token-tok-1')).toHaveTextContent(
      'inactive (creator no longer a Developer)',
    );
  });

  test('the expiry is chosen when the token is made, up to a year', async () => {
    const user = userEvent.setup();
    renderTokens([]);
    vi.mocked(api.post).mockResolvedValue({
      data: { ...tokenFixture({ id: 'tok-9', name: 'ci' }), token: 'excd_the-secret' },
    } as never);
    await user.type(await screen.findByTestId('site-deploy-token-name'), 'ci');
    await user.selectOptions(screen.getByTestId('site-deploy-token-expiry'), '365');
    await user.click(screen.getByTestId('site-deploy-token-create'));
    expect(api.post).toHaveBeenCalledWith(TOKENS_URL, { name: 'ci', expiresInDays: 365 });
  });
});
