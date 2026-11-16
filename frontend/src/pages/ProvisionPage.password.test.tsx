import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { ProvisionPage } from './ProvisionPage';
import { api } from '../api/client';
import { listMyOrgs } from '../api/orgs';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));
vi.mock('../api/orgs', () => ({ listMyOrgs: vi.fn() }));
const navigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => navigate };
});

const CATALOG = { documentDbRef: 'v', majors: [{ major: '16', available: true, documentDb: true }] };
const SHOWN = 'Shown-Once-Pass-77xQ';

function renderPage(flag = true) {
  vi.mocked(listMyOrgs).mockResolvedValue([{ id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: 'u1' }]);
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/config') return { data: { deploymentMode: 'cloud', features: { onetimecredentials: flag } } } as never;
    if (url === '/postgres/catalog') return { data: CATALOG } as never;
    if (url === '/tiers') return { data: [] } as never;
    if (url === '/provision/p-1/credentials') {
      return { data: { projectId: 'p-1', host: 'h', port: 5432, databaseName: 'appdb', username: 'app', sslMode: 'require', connectionUrl: 'x' } } as never;
    }
    throw new Error(`unexpected GET ${url}`);
  });
  vi.mocked(api.post).mockResolvedValue({ data: { projectId: 'p-1', password: SHOWN } } as never);
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
      <MemoryRouter><ProvisionPage /></MemoryRouter>
    </QueryClientProvider>,
  );
}

async function fillAndSubmit(user: ReturnType<typeof userEvent.setup>) {
  await user.type(screen.getByLabelText('Project Name'), 'my-db');
  await user.click(await screen.findByTestId('pg-version-16'));
  await user.click(screen.getByTestId('provision-submit'));
}

describe('ProvisionPage — database password', () => {
  beforeEach(() => { vi.clearAllMocks(); navigate.mockReset(); });

  test('sends no password when the field is blank', async () => {
    const user = userEvent.setup();
    renderPage();
    await screen.findByLabelText(/database password/i);
    await fillAndSubmit(user);
    await screen.findByTestId('otp-password');
    expect(vi.mocked(api.post).mock.calls[0][1]).not.toHaveProperty('password');
  });

  test('sends a typed password', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.type(await screen.findByLabelText(/database password/i), 'my chosen passphrase here');
    await fillAndSubmit(user);
    await screen.findByTestId('otp-password');
    expect(vi.mocked(api.post).mock.calls[0][1]).toMatchObject({ password: 'my chosen passphrase here' });
  });

  test('refuses a short password before sending', async () => {
    const user = userEvent.setup();
    renderPage();
    await user.type(await screen.findByLabelText(/database password/i), 'short');
    await fillAndSubmit(user);
    expect(await screen.findByTestId('password-error')).toHaveTextContent(/at least 16/i);
    expect(api.post).not.toHaveBeenCalled();
  });

  test('shows the password once before leaving, then navigates after it is saved', async () => {
    const user = userEvent.setup();
    const { container } = renderPage();
    await screen.findByLabelText(/database password/i);
    await fillAndSubmit(user);
    expect(await screen.findByTestId('otp-password')).toHaveTextContent(SHOWN);
    expect(navigate).not.toHaveBeenCalled();
    await user.click(screen.getByRole('button', { name: /i've saved it/i }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith('/project/p-1'));
    expect(container.innerHTML).not.toContain(SHOWN);
  });

  test('has no password field while the flag is off', async () => {
    renderPage(false);
    await screen.findByLabelText('Project Name');
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.queryByLabelText(/database password/i)).not.toBeInTheDocument();
  });
});
