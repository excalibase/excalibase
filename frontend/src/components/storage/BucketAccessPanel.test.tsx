import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { BucketAccessPanel, roleNameProblem } from './BucketAccessPanel';
import { api } from '../../api/client';
import type { BucketAccess } from '../../hooks/useStorage';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn(), delete: vi.fn() } }));

const ACCESS_URL = '/projects/p-1/storage/buckets/avatars/access';

function renderPanel(access: BucketAccess = {}) {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <BucketAccessPanel projectId="p-1" bucket="avatars" access={access} />
    </QueryClientProvider>,
  );
}

describe('Storage — app access rules', () => {
  beforeEach(() => vi.clearAllMocks());

  test('shows the rules the bucket has, one row per role', () => {
    renderPanel({ authenticated: { read: 'own', write: 'own' } });
    expect(screen.getByDisplayValue('authenticated')).toBeInTheDocument();
    expect(screen.getByLabelText('authenticated read')).toHaveValue('own');
    expect(screen.getByLabelText('authenticated write')).toHaveValue('own');
    expect(screen.getByLabelText('authenticated delete')).toHaveValue('');
  });

  test('says app users get nothing when there is no rule', () => {
    renderPanel();
    expect(screen.getByText(/App users cannot reach this bucket/)).toBeInTheDocument();
  });

  test('adding a role and saving sends the whole rule set', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: {} } as never);
    renderPanel({ authenticated: { read: 'own' } });
    await userEvent.click(screen.getByRole('button', { name: 'Add role' }));
    const roleInputs = screen.getAllByLabelText('Role name');
    await userEvent.type(roleInputs[1], 'staff');
    await userEvent.selectOptions(screen.getByLabelText('staff write'), 'all');
    await userEvent.click(screen.getByRole('button', { name: 'Save access' }));
    expect(api.put).toHaveBeenCalledWith(ACCESS_URL, {
      access: { authenticated: { read: 'own' }, staff: { write: 'all' } },
    });
    expect(await screen.findByText('Saved')).toBeInTheDocument();
  });

  test('removing the last role saves an empty rule set', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: {} } as never);
    renderPanel({ authenticated: { read: 'own' } });
    await userEvent.click(screen.getByRole('button', { name: 'Remove authenticated' }));
    await userEvent.click(screen.getByRole('button', { name: 'Save access' }));
    expect(api.put).toHaveBeenCalledWith(ACCESS_URL, { access: {} });
  });

  test('a bad or repeated role name is explained and nothing is sent', async () => {
    renderPanel({ authenticated: { read: 'own' } });
    await userEvent.click(screen.getByRole('button', { name: 'Add role' }));
    await userEvent.type(screen.getAllByLabelText('Role name')[1], 'Staff-Team');
    await userEvent.click(screen.getByRole('button', { name: 'Save access' }));
    expect(screen.getByRole('alert')).toHaveTextContent(/lowercase letters, digits and underscores/);
    await userEvent.clear(screen.getAllByLabelText('Role name')[1]);
    await userEvent.type(screen.getAllByLabelText('Role name')[1], 'authenticated');
    await userEvent.click(screen.getByRole('button', { name: 'Save access' }));
    expect(screen.getByRole('alert')).toHaveTextContent(/twice/);
    expect(api.put).not.toHaveBeenCalled();
  });

  test("a refusal shows the server's reason", async () => {
    vi.mocked(api.put).mockRejectedValue({ message: 'x', response: { status: 403, data: { error: 'insufficient project role' } } });
    renderPanel({ authenticated: { read: 'own' } });
    await userEvent.click(screen.getByRole('button', { name: 'Save access' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('insufficient project role');
  });
});

describe('roleNameProblem', () => {
  test.each([
    ['authenticated', null],
    ['staff_2', null],
    ['', 'Name the role.'],
    ['Admin', 'Use lowercase letters, digits and underscores, starting with a letter or underscore.'],
    ['9lives', 'Use lowercase letters, digits and underscores, starting with a letter or underscore.'],
  ])('%s', (name, problem) => {
    expect(roleNameProblem(name)).toBe(problem);
  });
});
