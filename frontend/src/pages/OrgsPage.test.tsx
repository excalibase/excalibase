import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { OrgsPage } from './OrgsPage';
import { listMyOrgs, createOrg } from '../api/orgs';

vi.mock('../api/orgs', () => ({ listMyOrgs: vi.fn(), createOrg: vi.fn() }));

const navigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => navigate };
});

function renderAt(path: string) {
  vi.mocked(listMyOrgs).mockResolvedValue([]);
  return render(
    <MemoryRouter initialEntries={[path]}>
      <OrgsPage />
    </MemoryRouter>,
  );
}

describe('OrgsPage — create', () => {
  beforeEach(() => vi.clearAllMocks());

  // The create-project form links here when the user has no organization.
  test('?new=1 opens the create form', async () => {
    renderAt('/orgs?new=1');
    expect(await screen.findByLabelText('Name')).toBeInTheDocument();
  });

  test('the name is limited to 64 characters and says so', async () => {
    renderAt('/orgs?new=1');
    const name = await screen.findByLabelText('Name');
    expect(name).toHaveAttribute('maxLength', '64');
    expect(screen.getByTestId('org-name-help')).toHaveTextContent('1–64 characters');
  });

  // The server's slug rule is 2–50 lowercase letters, digits and hyphens.
  test('a long name gives a slug capped at 50 characters that the server accepts', async () => {
    const user = userEvent.setup();
    vi.mocked(createOrg).mockResolvedValue({ id: 'o1', name: 'x', slug: 'x', tier: 'FREE' } as never);
    renderAt('/orgs?new=1');
    await user.type(await screen.findByLabelText('Name'), `${'a'.repeat(30)} ${'b'.repeat(24)}`);
    const slug = screen.getByLabelText('Slug') as HTMLInputElement;
    expect(slug.value.length).toBeLessThanOrEqual(50);
    expect(slug.value).toMatch(/^[a-z0-9][a-z0-9-]{1,49}$/);
    expect(slug.value.endsWith('-')).toBe(false);
    await user.click(screen.getByRole('button', { name: 'Create' }));
    expect(createOrg).toHaveBeenCalledWith(expect.any(String), slug.value);
  });

  test('a one-character name says the slug needs two characters and does not submit', async () => {
    const user = userEvent.setup();
    renderAt('/orgs?new=1');
    await user.type(await screen.findByLabelText('Name'), 'A');
    expect(screen.getByTestId('org-slug-error')).toHaveTextContent(/2–50/);
    expect(screen.getByRole('button', { name: 'Create' })).toBeDisabled();
  });

  test('a name with no Latin letters asks for a slug, and an entered one is used', async () => {
    const user = userEvent.setup();
    vi.mocked(createOrg).mockResolvedValue({ id: 'o1', name: '東京', slug: 'tokyo', tier: 'FREE' } as never);
    renderAt('/orgs?new=1');
    await user.type(await screen.findByLabelText('Name'), '東京');
    expect(screen.getByTestId('org-slug-error')).toHaveTextContent(/enter a slug/i);
    await user.type(screen.getByLabelText('Slug'), 'tokyo');
    expect(screen.queryByTestId('org-slug-error')).not.toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Create' }));
    expect(createOrg).toHaveBeenCalledWith('東京', 'tokyo');
  });

  test('an edited slug that breaks the rule says why', async () => {
    const user = userEvent.setup();
    renderAt('/orgs?new=1');
    await user.type(await screen.findByLabelText('Name'), 'Acme');
    const slug = screen.getByLabelText('Slug');
    await user.clear(slug);
    await user.type(slug, '-Bad_Slug');
    expect(screen.getByTestId('org-slug-error')).toHaveTextContent(/lowercase letters, digits and hyphens/);
    expect(screen.getByRole('button', { name: 'Create' })).toBeDisabled();
  });

  test("shows the server's refusal, such as the one-free-organization cap", async () => {
    const user = userEvent.setup();
    vi.mocked(createOrg).mockRejectedValue({
      response: { data: { error: 'you already own a free organization; each account can own one free organization' } },
    });
    renderAt('/orgs?new=1');
    await user.type(await screen.findByLabelText('Name'), 'Second');
    await user.click(screen.getByRole('button', { name: 'Create' }));
    expect(await screen.findByText(/each account can own one free organization/)).toBeInTheDocument();
  });
});
