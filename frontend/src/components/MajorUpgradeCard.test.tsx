import { describe, test, expect, beforeEach, afterEach, vi } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MajorUpgradeCard } from './MajorUpgradeCard';
import { api } from '../api/client';
import { useAuthStore } from '../stores/auth-store';
import type { DatabaseInstance, MajorUpgradeInfo, MajorUpgradeRun } from '../types';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const UPGRADE_URL = '/provision/p-1/major-upgrade';

const project = {
  projectId: 'p-1',
  projectName: 'shop',
  orgId: 'o1',
  status: 'ACTIVE',
  postgresVersion: '16',
} as DatabaseInstance;

function info(overrides: Partial<MajorUpgradeInfo> = {}): MajorUpgradeInfo {
  return {
    engine: 'postgresql',
    deploymentMode: 'k8s',
    currentVersion: '16',
    targets: ['17'],
    supported: true,
    reason: '',
    blockers: [],
    downtime: 'The database is offline for a few minutes while the data is converted.',
    latest: null,
    ...overrides,
  };
}

function run(overrides: Partial<MajorUpgradeRun> = {}): MajorUpgradeRun {
  return {
    id: 'mu-1',
    projectId: 'p-1',
    engine: 'postgresql',
    fromVersion: '16',
    toVersion: '17',
    status: 'RUNNING',
    step: 'UPGRADE_CONVERTING',
    safetyBackupId: 'backup-42',
    converted: false,
    warnings: [],
    startedAt: '2026-10-29T10:00:00Z',
    ...overrides,
  };
}

const refusal = (status: number, data: unknown) => Object.assign(new Error('x'), { response: { status, data } });

function renderCard({
  responses = [info()] as Array<MajorUpgradeInfo | Error>,
  orgRole = 'admin',
}: { responses?: Array<MajorUpgradeInfo | Error>; orgRole?: string } = {}) {
  let call = 0;
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === UPGRADE_URL) {
      const answer = responses[Math.min(call, responses.length - 1)];
      call += 1;
      return answer instanceof Error ? Promise.reject(answer) : Promise.resolve({ data: answer } as never);
    }
    if (url === '/provision/p-1') return Promise.resolve({ data: project } as never);
    if (url === '/orgs') return Promise.resolve({ data: [{ id: 'o1', name: 'O', slug: 'o', role: orgRole }] } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const invalidate = vi.spyOn(client, 'invalidateQueries');
  render(
    <QueryClientProvider client={client}>
      <MajorUpgradeCard project={project} />
    </QueryClientProvider>,
  );
  return { invalidate, upgradeCalls: () => vi.mocked(api.get).mock.calls.filter(([url]) => url === UPGRADE_URL).length };
}

describe('MajorUpgradeCard', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAuthStore.setState({ user: { id: 'u1', username: 'u', role: 'user' } as never });
  });
  afterEach(() => vi.useRealTimers());

  test('shows the current version, the downtime and what cannot be undone', async () => {
    renderCard();
    const card = await screen.findByTestId('major-upgrade-card');
    await waitFor(() => expect(card).toHaveTextContent('PostgreSQL 16'));
    expect(screen.getByTestId('major-upgrade-downtime')).toHaveTextContent('offline for a few minutes');
    const warning = screen.getByTestId('major-upgrade-warning');
    expect(warning).toHaveTextContent(/backup is taken first/i);
    expect(warning).toHaveTextContent(/cannot be undone/i);
    expect(warning).toHaveTextContent(/new project on PostgreSQL 16/i);
    expect(warning).toHaveTextContent(/point-in-time restore/i);
  });

  test('an unsupported project shows the reason and no controls', async () => {
    renderCard({
      responses: [info({ engine: 'mysql', supported: false, targets: [], reason: 'MySQL projects cannot be upgraded to a new major yet.' })],
    });
    expect(await screen.findByTestId('major-upgrade-reason')).toHaveTextContent('MySQL projects cannot be upgraded');
    expect(screen.queryByTestId('major-upgrade-btn')).not.toBeInTheDocument();
    expect(screen.queryByTestId('major-upgrade-target')).not.toBeInTheDocument();
  });

  test('a project on the newest major says so and offers nothing', async () => {
    renderCard({ responses: [info({ currentVersion: '18', targets: [] })] });
    expect(await screen.findByTestId('major-upgrade-reason')).toHaveTextContent(/already on the newest/i);
    expect(screen.queryByTestId('major-upgrade-btn')).not.toBeInTheDocument();
  });

  test('defaults the target to the newest major offered and sends the one chosen', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: { upgrade: run({ fromVersion: '15', toVersion: '16' }) } } as never);
    renderCard({ responses: [info({ currentVersion: '15', targets: ['16', '17'] })] });
    const select = await screen.findByTestId('major-upgrade-target');
    expect(select).toHaveValue('17');

    await user.selectOptions(select, '16');
    const button = screen.getByTestId('major-upgrade-btn');
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);
    await user.click(screen.getByTestId('modal-cancel'));
    expect(api.post).not.toHaveBeenCalled();

    await user.click(button);
    await user.type(screen.getByTestId('confirm-input'), 'shop');
    await user.click(screen.getByTestId('modal-confirm'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(UPGRADE_URL, { targetVersion: '16', confirm: true }),
    );
  });

  test('blockers are listed and keep the button disabled', async () => {
    renderCard({ responses: [info({ blockers: ['the extension "foo" is not offered on newer majors'] })] });
    const list = await screen.findByTestId('major-upgrade-blockers');
    expect(list).toHaveTextContent(/fixed before/i);
    expect(list).toHaveTextContent('the extension "foo" is not offered');
    await waitFor(() => expect(screen.getByTestId('major-upgrade-btn')).toBeDisabled());
  });

  test('a member below Admin sees the card but cannot start an upgrade', async () => {
    renderCard({ orgRole: 'developer' });
    expect(await screen.findByTestId('major-upgrade-admin-only')).toBeInTheDocument();
    expect(screen.getByTestId('major-upgrade-btn')).toBeDisabled();
  });

  test('starts only after the project name is typed exactly, and sends confirm', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: { upgrade: run({ step: 'UPGRADE_CHECKING' }) } } as never);
    renderCard();

    const button = await screen.findByTestId('major-upgrade-btn');
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);

    await user.type(screen.getByTestId('confirm-input'), 'sho');
    expect(screen.getByTestId('modal-confirm')).toBeDisabled();
    await user.type(screen.getByTestId('confirm-input'), 'p');
    await user.click(screen.getByTestId('modal-confirm'));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(UPGRADE_URL, { targetVersion: '17', confirm: true }),
    );
    expect(await screen.findByTestId('major-upgrade-running')).toHaveTextContent('Checking');
  });

  test('a refusal shows the server reason and its blockers', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue(
      refusal(409, { error: 'The project has blockers.', blockers: ['the extension "bar" is not offered'] }),
    );
    renderCard();

    const button = await screen.findByTestId('major-upgrade-btn');
    await waitFor(() => expect(button).toBeEnabled());
    await user.click(button);
    await user.type(screen.getByTestId('confirm-input'), 'shop');
    await user.click(screen.getByTestId('modal-confirm'));

    const error = await screen.findByTestId('major-upgrade-start-error');
    expect(error).toHaveTextContent('The project has blockers.');
    expect(error).toHaveTextContent('the extension "bar" is not offered');
  });

  test('a running upgrade shows its step, polls every 5 s and refreshes the project when it ends', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { invalidate, upgradeCalls } = renderCard({
      responses: [
        info({ latest: run({ step: 'UPGRADE_CONVERTING' }) }),
        info({ currentVersion: '17', targets: [], latest: run({ status: 'COMPLETED', step: 'UPGRADE_FINISHING' }) }),
      ],
    });

    expect(await screen.findByTestId('major-upgrade-running')).toHaveTextContent('Converting the data');
    expect(screen.queryByTestId('major-upgrade-btn')).not.toBeInTheDocument();
    expect(upgradeCalls()).toBe(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000);
    });

    expect(await screen.findByTestId('major-upgrade-completed')).toBeInTheDocument();
    expect(upgradeCalls()).toBe(2);
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['project', 'p-1'] });
  });

  test('a finished upgrade stops polling', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { upgradeCalls } = renderCard({
      responses: [info({ currentVersion: '17', targets: [], latest: run({ status: 'COMPLETED' }) })],
    });
    await screen.findByTestId('major-upgrade-completed');
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    expect(upgradeCalls()).toBe(1);
  });

  test('a completed upgrade names the new version and lists its warnings', async () => {
    renderCard({
      responses: [
        info({
          currentVersion: '17',
          targets: [],
          latest: run({ status: 'COMPLETED', warnings: ['statistics are still being rebuilt'] }),
        }),
      ],
    });
    const done = await screen.findByTestId('major-upgrade-completed');
    expect(done).toHaveTextContent('PostgreSQL 17');
    expect(done).toHaveTextContent('statistics are still being rebuilt');
  });

  test('a rolled back upgrade says the database is back on the old version', async () => {
    renderCard({
      responses: [
        info({ latest: run({ status: 'ROLLED_BACK', step: 'UPGRADE_ROLLING_BACK', error: 'The data could not be converted.' }) }),
      ],
    });
    const outcome = await screen.findByTestId('major-upgrade-rolled-back');
    expect(outcome).toHaveTextContent('The upgrade did not complete; the database was put back on PostgreSQL 16');
    expect(outcome).toHaveTextContent('The data could not be converted.');
  });

  test('a failed upgrade shows the error and the backup taken before it', async () => {
    renderCard({
      responses: [info({ latest: run({ status: 'FAILED', error: 'The database did not start on 17.' }) })],
    });
    const outcome = await screen.findByTestId('major-upgrade-failed');
    expect(outcome).toHaveAttribute('role', 'alert');
    expect(outcome).toHaveTextContent('The database did not start on 17.');
    expect(outcome).toHaveTextContent('backup-42');
  });

  test('a failed read shows the server reason', async () => {
    renderCard({ responses: [refusal(500, { error: 'The upgrade state could not be read.' })] });
    expect(await screen.findByTestId('major-upgrade-load-error')).toHaveTextContent('The upgrade state could not be read.');
  });
  test('a running run without a step yet still reads as working', async () => {
    renderCard({ responses: [info({ latest: run({ step: undefined }) }), info({ latest: run({ status: 'COMPLETED' }) })] });
    expect(await screen.findByTestId('major-upgrade-running')).toHaveTextContent('Upgrading to PostgreSQL 17: Working');
  });

  test('a project that is not running cannot be upgraded and says why', async () => {
    vi.mocked(api.get).mockReset();
    const paused = { ...project, status: 'PAUSED' } as DatabaseInstance;
    vi.mocked(api.get).mockImplementation((url: string) => {
      if (url === UPGRADE_URL) return Promise.resolve({ data: info() } as never);
      if (url === '/orgs') return Promise.resolve({ data: [{ id: 'o1', name: 'O', slug: 'o', role: 'admin' }] } as never);
      return Promise.resolve({ data: paused } as never);
    });
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MajorUpgradeCard project={paused} />
      </QueryClientProvider>,
    );
    expect(await screen.findByTestId('major-upgrade-not-active')).toBeInTheDocument();
    expect(screen.getByTestId('major-upgrade-btn')).toBeDisabled();
  });

  test('a refused start reloads what the card shows', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue(refusal(409, { error: 'a major upgrade of this project is already running' }));
    const { invalidate } = renderCard();
    await user.click(await screen.findByTestId('major-upgrade-btn'));
    await user.type(screen.getByTestId('confirm-input'), 'shop');
    await user.click(screen.getByTestId('modal-confirm'));
    expect(await screen.findByTestId('major-upgrade-start-error')).toHaveTextContent('already running');
    expect(invalidate).toHaveBeenCalledWith({ queryKey: expect.arrayContaining(['p-1']) });
  });
});
