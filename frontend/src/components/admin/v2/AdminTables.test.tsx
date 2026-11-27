import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { api } from '../../../api/client';
import { ProjectsAdminTable, projectColumns, PROJECT_FILTERS } from './ProjectsAdminTable';
import { UsersAdminTable, userColumns, USER_FILTERS } from './UsersAdminTable';
import { OrgsAdminTable, orgColumns, ORG_FILTERS } from './OrgsAdminTable';
import { ORG_SORTS, PROJECT_SORTS, USER_SORTS } from './listQuery';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn(), patch: vi.fn() } }));
vi.mock('../ProjectDetailPanel', () => ({ ProjectDetailPanel: ({ projectId }: { projectId: string }) => <div data-testid="panel">{projectId}</div> }));

const project = (n: number, extra: object = {}) => ({
  projectId: `p-${n}`, projectName: `Project ${n}`, orgId: 'o1', orgName: 'Acme', ownerId: `u-${n}`, ownerEmail: `dev${n}@acme.test`,
  ownerStatus: 'active', status: 'ACTIVE', tier: 'FREE', createdAt: '2026-11-01T00:00:00Z', deletionProtection: false, ...extra,
});
const user = (n: number, extra: object = {}) => ({
  id: `u-${n}`, email: `dev${n}@acme.test`, username: `dev${n}`, emailVerified: true, role: 'user', status: 'active',
  orgCount: 1, projectCount: 1, runningApps: 0, ...extra,
});

function renderWith(ui: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><MemoryRouter>{ui}</MemoryRouter></QueryClientProvider>);
}

const lastParams = (url: string) => {
  const calls = vi.mocked(api.get).mock.calls.filter(([called]) => called === url);
  return (calls.at(-1)?.[1] as { params: Record<string, unknown> } | undefined)?.params;
};

const sortableIds = (columns: { id: string; sortValue?: unknown }[]) => columns.filter((c) => c.sortValue).map((c) => c.id).sort();

describe('column definitions', () => {
  test('every sortable column is one the server allows, and every allowed sort has a column', () => {
    const sortable = (columns: { id: string; sortValue?: unknown }[]) => columns.filter((c) => c.sortValue).map((c) => c.id).sort();
    expect(sortable(projectColumns())).toEqual([...PROJECT_SORTS].sort());
    expect(sortable(userColumns())).toEqual([...USER_SORTS].sort());
    expect(sortable(orgColumns())).toEqual([...ORG_SORTS].sort());
  });

  test('the first column of each table names the row, so a checkbox reads "Select <name>"', () => {
    expect(projectColumns()[0].text?.(project(1) as never)).toBe('Project 1');
    expect(userColumns()[0].text?.(user(1) as never)).toBe('dev1@acme.test');
  });

  test('the credit column shows the balance, a dash for no account, and is absent while credits are off', () => {
    expect(orgColumns().map((c) => c.id)).not.toContain('credit');
    const credit = orgColumns(true).find((c) => c.id === 'credit');
    render(<>{credit?.cell({ creditMicros: 300_000_000 } as never)}</>);
    expect(screen.getByText('$300.00')).toBeInTheDocument();
    render(<>{credit?.cell({ creditMicros: 0 } as never)}</>);
    expect(screen.getByText('$0.00')).toBeInTheDocument();
    render(<>{credit?.cell({} as never)}</>);
    expect(screen.getByText('—')).toBeInTheDocument();
    expect(sortableIds(orgColumns(true))).toEqual([...ORG_SORTS].sort());
  });

  test('filter ids are the parameters the server reads', () => {
    expect(PROJECT_FILTERS.map((f) => f.id)).toEqual(['status', 'plan', 'owner', 'flag']);
    expect(USER_FILTERS.map((f) => f.id)).toEqual(['status', 'role']);
    expect(ORG_FILTERS.map((f) => f.id)).toEqual(['tier', 'ownerStatus']);
  });

  test('a project row without a name shows its id, and a suspended owner is flagged', () => {
    const cells = projectColumns();
    render(<>{cells[0].cell(project(1, { projectName: '' }) as never)}</>);
    expect(screen.getAllByText('p-1').length).toBeGreaterThan(0);
    render(<>{cells[2].cell(project(2, { ownerSuspended: true }) as never)}</>);
    expect(screen.getByText('Suspended')).toBeInTheDocument();
  });
});

describe('ProjectsAdminTable', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(api.get).mockImplementation((_url: string, config?: unknown) => {
      const offset = (config as { params: { offset: number } }).params.offset;
      return Promise.resolve({ data: { items: [project(offset + 1), project(offset + 2)], total: 90 } } as never);
    });
  });

  test('shows one page of the total and asks the server for the next', async () => {
    renderWith(<ProjectsAdminTable canMutate />);
    expect(await screen.findByText('Project 1')).toBeInTheDocument();
    expect(screen.getByText('1–25 of 90')).toBeInTheDocument();
    expect(lastParams('/admin/projects')).toMatchObject({ limit: 25, offset: 0 });
    await userEvent.click(screen.getByRole('button', { name: 'Next page' }));
    await screen.findByText('Project 26');
    expect(lastParams('/admin/projects')).toMatchObject({ limit: 25, offset: 25 });
  });

  test('a filter and a sort go to the server and return to page 1', async () => {
    renderWith(<ProjectsAdminTable canMutate />);
    await screen.findByText('Project 1');
    await userEvent.click(screen.getByRole('button', { name: 'Next page' }));
    await screen.findByText('Project 26');
    await userEvent.selectOptions(screen.getByLabelText('Plan'), 'STANDARD');
    await waitFor(() => expect(lastParams('/admin/projects')).toMatchObject({ plan: 'STANDARD', offset: 0 }));
    await userEvent.click(screen.getByRole('button', { name: 'Created' }));
    await waitFor(() => expect(lastParams('/admin/projects')).toMatchObject({ sort: 'created', order: 'asc' }));
  });

  test('bulk delete asks for a reason, then drops each project with it', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: {}, status: 202 } as never);
    renderWith(<ProjectsAdminTable canMutate />);
    await screen.findByText('Project 1');
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select Project 1' }));
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select Project 2' }));
    await userEvent.click(screen.getByRole('button', { name: 'Delete…' }));
    const dialog = screen.getByRole('dialog');
    const confirm = within(dialog).getByRole('button', { name: 'Delete' });
    expect(confirm).toBeDisabled();
    await userEvent.type(within(dialog).getByLabelText('Reason'), 'abandoned test projects');
    await userEvent.click(confirm);
    expect(await screen.findByText('Deleted 2 of 2 projects.')).toBeInTheDocument();
    expect(api.delete).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.delete).mock.calls[0]).toEqual(['/admin/projects/p-1', expect.objectContaining({ data: { reason: 'abandoned test projects' } })]);
  });

  test('a failed item is named in the outcome and the others still go through', async () => {
    vi.mocked(api.delete)
      .mockRejectedValueOnce({ response: { status: 409, data: { error: 'a teardown is already running' } } })
      .mockResolvedValueOnce({ data: {} } as never);
    renderWith(<ProjectsAdminTable canMutate />);
    await screen.findByText('Project 1');
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select all rows on this page' }));
    await userEvent.click(screen.getByRole('button', { name: 'Delete…' }));
    await userEvent.type(screen.getByLabelText('Reason'), 'cleaning up the demo org');
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Delete' }));
    expect(await screen.findByText('Deleted 1 of 2 projects.')).toBeInTheDocument();
    expect(screen.getByText('Project 1: a teardown is already running')).toBeInTheDocument();
  });

  test('suspend owners suspends each owner once', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { items: [project(1, { ownerId: 'same' }), project(2, { ownerId: 'same' })], total: 2 } } as never);
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    renderWith(<ProjectsAdminTable canMutate />);
    await screen.findByText('Project 1');
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select all rows on this page' }));
    await userEvent.click(screen.getByRole('button', { name: 'Suspend owners…' }));
    await userEvent.type(screen.getByLabelText('Reason'), 'proxy abuse from both projects');
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Suspend' }));
    await screen.findByText('Suspended 1 of 1 accounts.');
    expect(api.post).toHaveBeenCalledTimes(1);
    expect(api.post).toHaveBeenCalledWith('/admin/console/users/same/suspend', { reason: 'proxy abuse from both projects' });
  });

  test('a read-only operator has no checkboxes and a disabled Drop', async () => {
    renderWith(<ProjectsAdminTable canMutate={false} />);
    await screen.findByText('Project 1');
    expect(screen.queryByRole('checkbox')).toBeNull();
    expect(screen.getAllByRole('button', { name: /Drop/ })[0]).toBeDisabled();
    expect(screen.queryByRole('button', { name: /Open/ })).toBeNull();
  });

  test('a server error shows the retry panel, not an empty list', async () => {
    vi.mocked(api.get).mockRejectedValue({ response: { status: 500, data: {} } });
    renderWith(<ProjectsAdminTable canMutate />);
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not load projects');
  });
});

describe('UsersAdminTable', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: { items: [user(1), user(2, { status: 'suspended', suspendedReason: 'proxy abuse' }), user(3, { role: 'platform_admin' })], total: 3 } } as never);
  });

  test('lists every account including suspended ones, filtered on the server', async () => {
    renderWith(<UsersAdminTable />);
    await screen.findByText('dev1@acme.test');
    expect(lastParams('/admin/console/users')).toMatchObject({ includeSuspended: 'true', limit: 25 });
    await userEvent.selectOptions(screen.getByLabelText('Status'), 'suspended');
    await waitFor(() => expect(lastParams('/admin/console/users')).toMatchObject({ status: 'suspended' }));
  });

  test('bulk suspend skips platform staff and asks for a reason', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    renderWith(<UsersAdminTable />);
    await screen.findByText('dev1@acme.test');
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select all rows on this page' }));
    await userEvent.click(screen.getByRole('button', { name: 'Suspend…' }));
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByRole('button', { name: 'Suspend' })).toBeDisabled();
    await userEvent.type(within(dialog).getByLabelText('Reason'), 'throwaway sign-ups');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Suspend' }));
    await screen.findByText('Suspended 1 of 1 accounts.');
    expect(api.post).toHaveBeenCalledWith('/admin/console/users/u-1/suspend', { reason: 'throwaway sign-ups' });
  });

  test('the bulk dialog names the accounts it leaves out and why', async () => {
    renderWith(<UsersAdminTable />);
    await screen.findByText('dev1@acme.test');
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select all rows on this page' }));
    await userEvent.click(screen.getByRole('button', { name: 'Suspend…' }));
    const dialog = screen.getByRole('dialog');
    expect(dialog).toHaveTextContent('dev3@acme.test (platform staff)');
    expect(dialog).toHaveTextContent('dev2@acme.test (already suspended)');
  });

  test('a bulk suspend whose apps did not all stop reports the account as partly done, naming the apps', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { userId: 'u-1', status: 'suspended', appsStopped: [], appsRestored: [], appsFailed: [{ appName: 'proxy', reason: 'cluster busy' }] } } as never);
    renderWith(<UsersAdminTable />);
    await screen.findByText('dev1@acme.test');
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select all rows on this page' }));
    await userEvent.click(screen.getByRole('button', { name: 'Suspend…' }));
    const dialog = screen.getByRole('dialog');
    await userEvent.type(within(dialog).getByLabelText('Reason'), 'confirmed abuse ring');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Suspend' }));
    expect(await screen.findByText(/dev1@acme.test: .*proxy.*cluster busy/)).toBeInTheDocument();
  });

  test('one account is suspended from its row with a reason', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { userId: 'u-1', status: 'suspended', appsStopped: [], appsRestored: [], appsFailed: [] } } as never);
    renderWith(<UsersAdminTable />);
    const row = (await screen.findByText('dev1@acme.test')).closest('tr')!;
    await userEvent.click(within(row).getByRole('button', { name: 'Suspend' }));
    const dialog = screen.getByRole('dialog');
    await userEvent.type(within(dialog).getByLabelText('Reason'), 'proxy traffic');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Suspend' }));
    expect(await screen.findByText(/Account is now suspended/)).toBeInTheDocument();
  });
});

describe('OrgsAdminTable', () => {
  const org = { id: 'o1', name: 'Acme', slug: 'acme', tier: 'FREE', ownerId: 'u1', ownerEmail: 'owner@acme.test', ownerStatus: 'active', projectCount: 2, runningApps: 1, cpuCores: 0.5, memoryMb: 300 };
  beforeEach(() => {
    vi.resetAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: { items: [org, { ...org, id: 'o2', name: 'Beta', slug: 'beta' }], total: 100 } } as never);
  });

  test('pages the organizations and filters by plan on the server', async () => {
    renderWith(<OrgsAdminTable canMutate />);
    await screen.findByText('Acme');
    expect(screen.getByText('1–25 of 100')).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText('Plan'), 'STANDARD');
    await waitFor(() => expect(lastParams('/admin/console/orgs')).toMatchObject({ tier: 'STANDARD', offset: 0 }));
  });

  test('the table shows the balance column only while the credits feature is on', async () => {
    const reply = (credits: boolean) => (url: string) => Promise.resolve({
      data: url === '/config' ? { deploymentMode: 'cloud', features: { credits } } : { items: [{ ...org, creditMicros: 42_000_000 }], total: 1 },
    } as never);
    vi.mocked(api.get).mockImplementation(reply(true));
    const on = renderWith(<OrgsAdminTable canMutate />);
    expect(await screen.findByText('$42.00')).toBeInTheDocument();
    on.unmount();
    vi.mocked(api.get).mockImplementation(reply(false));
    renderWith(<OrgsAdminTable canMutate />);
    await screen.findByText('Acme');
    expect(screen.queryByText('$42.00')).toBeNull();
  });

  test('the budget column shows CPU and memory used against the plan, and only while the budget is on', async () => {
    const budget = { cpuMilliUsed: 500, cpuMilliLimit: 2000, memoryBytesUsed: 1024 ** 3, memoryBytesLimit: 4 * 1024 ** 3 };
    expect(orgColumns().map((c) => c.id)).not.toContain('budget');
    const column = orgColumns(false, true).find((c) => c.id === 'budget');
    render(<>{column?.cell({ budget } as never)}</>);
    expect(screen.getByText('0.5 of 2 vCPU · 1 of 4 GiB')).toBeInTheDocument();
    render(<>{column?.cell({} as never)}</>);
    expect(screen.getByText('—')).toBeInTheDocument();
    const reply = (planbudget: boolean) => (url: string) => Promise.resolve({
      data: url === '/config' ? { deploymentMode: 'cloud', features: { planbudget } } : { items: [{ ...org, budget }], total: 1 },
    } as never);
    vi.mocked(api.get).mockImplementation(reply(true));
    const on = renderWith(<OrgsAdminTable canMutate />);
    expect(await screen.findByText('0.5 of 2 vCPU · 1 of 4 GiB', { selector: 'td *, td' })).toBeInTheDocument();
    on.unmount();
    vi.mocked(api.get).mockImplementation(reply(false));
    renderWith(<OrgsAdminTable canMutate />);
    await screen.findByText('Acme');
    expect(screen.queryAllByText('0.5 of 2 vCPU · 1 of 4 GiB')).toHaveLength(1);
  });

  test('revoking needs a reason and sends it with the cascade', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: {} } as never);
    renderWith(<OrgsAdminTable canMutate />);
    const row = (await screen.findByText('Acme')).closest('tr')!;
    await userEvent.click(within(row).getByRole('button', { name: /Revoke/ }));
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByRole('button', { name: 'Revoke' })).toBeDisabled();
    await userEvent.type(within(dialog).getByLabelText('Reason'), 'confirmed abuse ring');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Revoke' }));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/admin/orgs/o1', { params: { cascade: true }, data: { reason: 'confirmed abuse ring' } }));
  });

  test('a revoke answered 207 with failed projects is shown as failed, with the projects named', async () => {
    vi.mocked(api.delete).mockResolvedValue({ status: 207, data: { orgId: 'o1', dropped: 1, failed: ['p-9'] } } as never);
    renderWith(<OrgsAdminTable canMutate />);
    const row = (await screen.findByText('Acme')).closest('tr')!;
    await userEvent.click(within(row).getByRole('button', { name: /Revoke/ }));
    const dialog = screen.getByRole('dialog');
    await userEvent.type(within(dialog).getByLabelText('Reason'), 'confirmed abuse ring');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Revoke' }));
    expect(await within(screen.getByRole('dialog')).findByText(/p-9/)).toBeInTheDocument();
  });

  test('a bulk revoke with a 207 counts that org as failed', async () => {
    vi.mocked(api.delete).mockImplementation(((url: string) => Promise.resolve(url.endsWith('/o2')
      ? { status: 207, data: { failed: ['p-9'] } } : { status: 200, data: { failed: [] } })) as never);
    renderWith(<OrgsAdminTable canMutate />);
    await screen.findByText('Acme');
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select all rows on this page' }));
    await userEvent.click(screen.getByRole('button', { name: 'Revoke…' }));
    await userEvent.type(screen.getByLabelText('Reason'), 'confirmed abuse ring');
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Revoke' }));
    expect(await screen.findByText('Revoked 1 of 2 organizations.')).toBeInTheDocument();
    expect(screen.getByText(/beta: .*p-9/)).toBeInTheDocument();
  });

  test('bulk revoke goes through a reason too', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: {} } as never);
    renderWith(<OrgsAdminTable canMutate />);
    await screen.findByText('Acme');
    await userEvent.click(screen.getByRole('checkbox', { name: 'Select all rows on this page' }));
    await userEvent.click(screen.getByRole('button', { name: 'Revoke…' }));
    await userEvent.type(screen.getByLabelText('Reason'), 'confirmed abuse ring');
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Revoke' }));
    expect(await screen.findByText('Revoked 2 of 2 organizations.')).toBeInTheDocument();
  });
});
