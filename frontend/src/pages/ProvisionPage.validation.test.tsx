import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { ProvisionPage } from './ProvisionPage';
import { api } from '../api/client';
import { listMyOrgs, type Org } from '../api/orgs';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));
vi.mock('../api/orgs', () => ({ listMyOrgs: vi.fn() }));

const navigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => navigate };
});

const CATALOG = { documentDbRef: 'v0.117-0', majors: [{ major: '16', available: true, documentDb: true }] };
const ACME: Org = { id: 'org-1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: 'u1' };

function renderPage(orgs: Org[]) {
  vi.mocked(listMyOrgs).mockResolvedValue(orgs);
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/postgres/catalog') return Promise.resolve({ data: CATALOG } as never);
    if (url === '/tiers') return Promise.resolve({ data: [] } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  vi.mocked(api.post).mockResolvedValue({ data: { projectId: 'p-1' } } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <ProvisionPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

// EXC-553: a create that cannot go ahead says what is missing, next to the
// field, instead of a button that silently does nothing.
describe('ProvisionPage — missing fields', () => {
  beforeEach(() => vi.clearAllMocks());

  test('the button can be pressed and names every missing field', async () => {
    const user = userEvent.setup();
    renderPage([ACME, { ...ACME, id: 'org-2', name: 'Other' }]);
    await screen.findByTestId('pg-version-16');

    expect(screen.getByTestId('provision-submit')).toBeEnabled();
    await user.click(screen.getByTestId('provision-submit'));

    expect(screen.getByTestId('project-name-error')).toHaveTextContent(/project name/i);
    expect(screen.getByTestId('org-error')).toHaveTextContent(/choose an organization/i);
    expect(screen.getByTestId('version-error')).toHaveTextContent(/postgresql version/i);
    expect(screen.getByLabelText('Project Name')).toHaveAttribute('aria-invalid', 'true');
    expect(api.post).not.toHaveBeenCalled();
  });

  test('a project name over the server limit is refused before sending', async () => {
    const user = userEvent.setup();
    renderPage([ACME]);
    await screen.findByTestId('pg-version-16');

    await user.type(screen.getByLabelText('Project Name'), 'x'.repeat(101));
    await user.click(screen.getByTestId('pg-version-16'));
    await user.click(screen.getByTestId('provision-submit'));

    expect(screen.getByTestId('project-name-error')).toHaveTextContent('100 characters or fewer');
    expect(api.post).not.toHaveBeenCalled();
  });

  test('a fixed field clears its error and the project is created', async () => {
    const user = userEvent.setup();
    renderPage([ACME]);
    await screen.findByTestId('pg-version-16');

    await user.click(screen.getByTestId('provision-submit'));
    expect(screen.getByTestId('project-name-error')).toBeInTheDocument();

    await user.type(screen.getByLabelText('Project Name'), 'shop');
    expect(screen.queryByTestId('project-name-error')).not.toBeInTheDocument();
    await user.click(screen.getByTestId('pg-version-16'));
    await user.click(screen.getByTestId('provision-submit'));

    expect(api.post).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.post).mock.calls[0][1]).toMatchObject({ projectName: 'shop', orgId: 'org-1' });
  });
});

describe('ProvisionPage — no organization yet', () => {
  beforeEach(() => vi.clearAllMocks());

  test('explains that a project lives in an organization and links to create one', async () => {
    renderPage([]);
    const notice = await screen.findByTestId('provision-no-orgs');
    expect(notice).toHaveTextContent(/organization/i);
    expect(screen.getByRole('link', { name: /create an organization/i })).toHaveAttribute('href', '/orgs?new=1');
  });

  test('pressing create says the organization is required, with the same link', async () => {
    const user = userEvent.setup();
    renderPage([]);
    await screen.findByTestId('provision-no-orgs');
    await user.type(screen.getByLabelText('Project Name'), 'shop');
    await user.click(screen.getByTestId('engine-NONE'));
    await user.click(screen.getByTestId('provision-submit'));

    const error = screen.getByTestId('org-error');
    expect(error).toHaveTextContent(/organization is required/i);
    expect(error.querySelector('a')).toHaveAttribute('href', '/orgs?new=1');
    expect(api.post).not.toHaveBeenCalled();
  });
});
