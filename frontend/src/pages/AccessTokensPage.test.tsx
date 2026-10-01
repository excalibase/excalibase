import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { AccessTokensPage } from './AccessTokensPage';
import { api } from '../api/client';
import { scopeLabel } from '../api/accessTokens';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

const TOKENS_PATH = '/auth/tokens';
const PROJECTS_PATH = '/provision';

const listed = [
  {
    id: 'hash-ci',
    tokenPrefix: 'excb_1a2b3c4',
    name: 'ci deploy',
    scopes: 'read',
    projectId: 'proj-1',
    createdAt: '2026-09-27T01:00:00Z',
    expiresAt: '2026-12-26T01:00:00Z',
    lastUsed: '2026-10-01T08:00:00Z',
  },
  { id: 'hash-old', tokenPrefix: 'excb_9f8e7d6', name: 'old script', scopes: '', createdAt: '2026-09-01T01:00:00Z' },
];

const projects = [
  { projectId: 'proj-1', projectName: 'storefront' },
  { projectId: 'proj-2', projectName: 'analytics' },
];

function mockGets(tokens: unknown = listed) {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === TOKENS_PATH) return { data: tokens } as never;
    if (url === PROJECTS_PATH) return { data: projects } as never;
    throw new Error(`unexpected GET ${url}`);
  });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <AccessTokensPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const created = {
  token: 'excb_FULLSECRETVALUE',
  prefix: 'excb_FULLSEC',
  name: 'storefront seed',
  scopes: 'read',
  projectId: 'proj-1',
  expiresAt: '2026-11-01T00:00:00Z',
};

describe('Access tokens', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockGets();
  });

  test('lists each token with its scope, project, dates and last use — never a secret', async () => {
    renderPage();
    const row = await screen.findByTestId('access-token-hash-ci');
    expect(row).toHaveTextContent('ci deploy');
    expect(row).toHaveTextContent('Read only');
    expect(row).toHaveTextContent('storefront');
    expect(row).toHaveTextContent('excb_1a2b3c4');
    expect(within(row).getByTestId('token-created')).toHaveTextContent('2026');
    expect(within(row).getByTestId('token-last-used')).toHaveTextContent('2026');
    expect(within(row).getByTestId('token-expires')).toHaveTextContent('2026');
    expect(api.get).toHaveBeenCalledWith(TOKENS_PATH);
  });

  test('a token never used, with no expiry and no project, says so', async () => {
    renderPage();
    const row = await screen.findByTestId('access-token-hash-old');
    expect(within(row).getByTestId('token-last-used')).toHaveTextContent('Never');
    expect(within(row).getByTestId('token-expires')).toHaveTextContent('Never');
    expect(row).toHaveTextContent('All your projects');
    expect(row).toHaveTextContent('Full access (legacy)');
  });

  test('an expired token is marked expired', async () => {
    mockGets([{ ...listed[0], expiresAt: '2020-01-01T00:00:00Z' }]);
    renderPage();
    const row = await screen.findByTestId('access-token-hash-ci');
    expect(within(row).getByTestId('token-expires')).toHaveTextContent('Expired');
  });

  test('the empty state explains that a password reset revokes every token', async () => {
    mockGets([]);
    renderPage();
    expect(await screen.findByText(/no access tokens/i)).toBeInTheDocument();
    expect(screen.getByText(/password reset revokes every access token/i)).toBeInTheDocument();
  });

  test('creating a read-only, project-bound token sends exactly that and shows the secret once', async () => {
    const u = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    vi.mocked(api.post).mockResolvedValue({ data: created } as never);
    renderPage();
    await screen.findByTestId('access-token-hash-ci');

    await u.type(screen.getByLabelText('Token name'), '  storefront seed ');
    await u.selectOptions(screen.getByLabelText('Access'), 'read');
    await u.selectOptions(screen.getByLabelText('Project'), 'proj-1');
    await u.selectOptions(screen.getByLabelText('Expires'), '30d');
    await u.click(screen.getByRole('button', { name: /create token/i }));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(TOKENS_PATH, {
        name: 'storefront seed',
        scopes: ['read'],
        expiresIn: '30d',
        projectId: 'proj-1',
      }),
    );
    const shown = await screen.findByTestId('new-access-token');
    expect(shown).toHaveTextContent('excb_FULLSECRETVALUE');
    expect(shown).toHaveTextContent(/will not be shown again/i);

    await u.click(within(shown).getByRole('button', { name: /copy token/i }));
    expect(writeText).toHaveBeenCalledWith('excb_FULLSECRETVALUE');
    expect(await within(shown).findByText(/copied/i)).toBeInTheDocument();

    await u.click(within(shown).getByRole('button', { name: /i have saved it/i }));
    expect(screen.queryByText('excb_FULLSECRETVALUE')).not.toBeInTheDocument();
  });

  test('a second token while the first is still shown starts uncopied', async () => {
    const u = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockResolvedValue(undefined) }, configurable: true });
    vi.mocked(api.post)
      .mockResolvedValueOnce({ data: created } as never)
      .mockResolvedValueOnce({ data: { ...created, token: 'excb_SECONDSECRET', prefix: 'excb_SECONDS', name: 'second' } } as never);
    renderPage();
    await screen.findByTestId('access-token-hash-ci');
    await u.type(screen.getByLabelText('Token name'), 'first');
    await u.click(screen.getByRole('button', { name: /create token/i }));
    await u.click(within(await screen.findByTestId('new-access-token')).getByRole('button', { name: /copy token/i }));
    expect(await screen.findByText(/copied/i)).toBeInTheDocument();

    await u.type(screen.getByLabelText('Token name'), 'second');
    await u.click(screen.getByRole('button', { name: /create token/i }));
    expect(await screen.findByText('excb_SECONDSECRET')).toBeInTheDocument();
    expect(screen.queryByText(/copied/i)).not.toBeInTheDocument();
  });

  test('read and write, every project, no expiry: the request asks for write and never', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: { ...created, scopes: 'read,write', projectId: '', expiresAt: null } } as never);
    renderPage();
    await screen.findByTestId('access-token-hash-ci');

    await u.type(screen.getByLabelText('Token name'), 'deploy');
    await u.selectOptions(screen.getByLabelText('Access'), 'write');
    await u.selectOptions(screen.getByLabelText('Expires'), 'never');
    expect(screen.getByText(/never expires/i)).toBeInTheDocument();
    await u.click(screen.getByRole('button', { name: /create token/i }));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(TOKENS_PATH, { name: 'deploy', scopes: ['read', 'write'], expiresIn: 'never' }),
    );
  });

  test('the project choice offers only the projects the caller can see', async () => {
    renderPage();
    await screen.findByTestId('access-token-hash-ci');
    const options = within(screen.getByLabelText('Project')).getAllByRole('option').map((o) => o.textContent);
    expect(options).toEqual(['All your projects', 'storefront', 'analytics']);
  });

  test('the access choice offers only read and read-and-write', async () => {
    renderPage();
    await screen.findByTestId('access-token-hash-ci');
    const values = within(screen.getByLabelText('Access')).getAllByRole('option').map((o) => (o as HTMLOptionElement).value);
    expect(values).toEqual(['read', 'write']);
  });

  test('a name is required before anything is sent', async () => {
    const u = userEvent.setup();
    renderPage();
    await screen.findByTestId('access-token-hash-ci');
    await u.click(screen.getByRole('button', { name: /create token/i }));
    expect(await screen.findByText(/give the token a name/i)).toBeInTheDocument();
    expect(api.post).not.toHaveBeenCalled();
  });

  test('a refused creation shows the server reason and no secret', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({ response: { status: 429, data: { error: 'rate limit exceeded' } } });
    renderPage();
    await screen.findByTestId('access-token-hash-ci');
    await u.type(screen.getByLabelText('Token name'), 'bulk');
    await u.click(screen.getByRole('button', { name: /create token/i }));
    expect(await screen.findByText('rate limit exceeded')).toBeInTheDocument();
    expect(screen.queryByTestId('new-access-token')).not.toBeInTheDocument();
  });

  test('a failed copy says so instead of pretending it worked', async () => {
    const u = userEvent.setup();
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn().mockRejectedValue(new Error('denied')) },
      configurable: true,
    });
    vi.mocked(api.post).mockResolvedValue({ data: created } as never);
    renderPage();
    await screen.findByTestId('access-token-hash-ci');
    await u.type(screen.getByLabelText('Token name'), 'x');
    await u.click(screen.getByRole('button', { name: /create token/i }));
    const shown = await screen.findByTestId('new-access-token');
    await u.click(within(shown).getByRole('button', { name: /copy token/i }));
    expect(await within(shown).findByText(/copy it by hand/i)).toBeInTheDocument();
  });

  test('revoking asks first; cancelling sends nothing', async () => {
    const u = userEvent.setup();
    renderPage();
    const row = await screen.findByTestId('access-token-hash-ci');
    await u.click(within(row).getByRole('button', { name: /revoke/i }));
    expect(screen.getByTestId('confirm-modal')).toHaveTextContent('ci deploy');
    await u.click(screen.getByTestId('modal-cancel'));
    expect(api.delete).not.toHaveBeenCalled();
  });

  test('a confirmed revoke deletes by the token id and refreshes the list', async () => {
    const u = userEvent.setup();
    vi.mocked(api.delete).mockResolvedValue({ data: { status: 'revoked' } } as never);
    renderPage();
    const row = await screen.findByTestId('access-token-hash-ci');
    await u.click(within(row).getByRole('button', { name: /revoke/i }));
    await u.click(screen.getByTestId('modal-confirm'));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith(`${TOKENS_PATH}/hash-ci`));
    await waitFor(() => expect(vi.mocked(api.get).mock.calls.filter(([url]) => url === TOKENS_PATH).length).toBeGreaterThan(1));
  });

  test('a failed revoke shows why', async () => {
    const u = userEvent.setup();
    vi.mocked(api.delete).mockRejectedValue({ response: { data: { error: 'forbidden' } } });
    renderPage();
    const row = await screen.findByTestId('access-token-hash-ci');
    await u.click(within(row).getByRole('button', { name: /revoke/i }));
    await u.click(screen.getByTestId('modal-confirm'));
    expect(await screen.findByText('forbidden')).toBeInTheDocument();
  });

  test('a list that cannot load says so', async () => {
    vi.mocked(api.get).mockImplementation(async (url: string) => {
      if (url === TOKENS_PATH) throw { response: { data: { error: 'failed to list tokens' } } };
      return { data: projects } as never;
    });
    renderPage();
    expect(await screen.findByText('failed to list tokens')).toBeInTheDocument();
  });
});

describe('scopeLabel', () => {
  test.each([
    ['read', 'Read only'],
    ['read,write', 'Read and write'],
    ['write', 'Read and write'],
    ['admin', 'Read and write'],
    ['', 'Full access (legacy)'],
  ])('%j reads as %s', (scopes, label) => {
    expect(scopeLabel(scopes)).toBe(label);
  });
});
