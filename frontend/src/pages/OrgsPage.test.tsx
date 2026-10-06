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
