import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AppDiskCard } from './AppDiskCard';
import { api } from '../../api/client';
import type { App, AppDisk, AppDiskStatus } from '../../api/apps';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const MI = 1024 ** 2;
const GI = 1024 ** 3;
const DISK_URL = '/projects/proj-1/apps/app-1/disk';

const disk: AppDisk = { mountPath: '/data', size: '5Gi' };

const app: App & { disk: AppDisk } = {
  id: 'app-1',
  projectId: 'proj-1',
  name: 'web',
  image: 'nginx:1.27',
  env: [],
  port: 8080,
  replicas: 1,
  disk,
  tier: 'STANDARD',
  status: 'ACTIVE',
  version: 3,
  createdAt: '2026-09-20T10:00:00Z',
  updatedAt: '2026-09-20T10:00:00Z',
};

const measured: AppDiskStatus = {
  mountPath: '/data',
  size: '5Gi',
  sizeBytes: 5 * GI,
  usedBytes: 120 * MI,
  filesystemBytes: 5 * GI,
  planMax: '20Gi',
  planMaxBytes: 20 * GI,
  overPlan: false,
  measuredAt: '2026-09-28T10:00:00Z',
};

function mount(overrides: Partial<App & { disk: AppDisk }> = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <AppDiskCard app={{ ...app, ...overrides }} />
    </QueryClientProvider>,
  );
  return userEvent.setup();
}

function renderCard(
  overrides: Partial<App & { disk: AppDisk }> = {},
  status: Partial<AppDiskStatus> = {},
) {
  vi.mocked(api.get).mockResolvedValue({ data: { ...measured, ...status } } as never);
  return mount(overrides);
}

const refusal = (status: number, error: string) => ({ response: { status, data: { error } } });

async function enterSize(user: ReturnType<typeof userEvent.setup>, amount: string, unit: string) {
  const size = await screen.findByTestId('disk-resize-size');
  await user.clear(size);
  await user.type(size, amount);
  await user.selectOptions(screen.getByTestId('disk-resize-unit'), unit);
}

describe('AppDiskCard', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.post).mockReset();
  });

  test('shows what the disk holds against its size and the plan cap', async () => {
    renderCard();
    const usage = await screen.findByTestId('disk-usage');
    expect(usage).toHaveTextContent('120Mi used of 5Gi');
    expect(screen.getByRole('meter')).toHaveAttribute('aria-valuenow', '2');
    expect(screen.getByTestId('disk-plan-cap')).toHaveTextContent('up to 20Gi');
    expect(api.get).toHaveBeenCalledWith(DISK_URL);
  });

  test('a disk no deploy has made yet says so', async () => {
    renderCard({}, { usedBytes: undefined, filesystemBytes: undefined, measuredAt: undefined });
    expect(await screen.findByTestId('disk-usage')).toHaveTextContent(/not created yet/i);
    expect(screen.queryByRole('meter')).not.toBeInTheDocument();
  });

  test('refresh measures the disk again', async () => {
    const user = renderCard();
    await screen.findByTestId('disk-usage');
    vi.mocked(api.get).mockResolvedValueOnce({ data: { ...measured, usedBytes: GI } } as never);
    await user.click(screen.getByTestId('disk-refresh'));
    await waitFor(() => expect(screen.getByTestId('disk-usage')).toHaveTextContent('1Gi used'));
    expect(api.get).toHaveBeenCalledTimes(2);
  });

  test('a disk busy with another operation asks to retry', async () => {
    vi.mocked(api.get).mockRejectedValue(refusal(409, 'the app is being deployed'));
    mount();
    expect(await screen.findByTestId('disk-usage-error')).toHaveTextContent(/busy.*retry/i);
  });

  test('a failed measurement shows the server message', async () => {
    vi.mocked(api.get).mockRejectedValue(refusal(502, 'the disk probe did not finish'));
    mount();
    expect(await screen.findByTestId('disk-usage-error')).toHaveTextContent(
      'the disk probe did not finish',
    );
  });

  test('a disk above the plan warns what the next deploy does', async () => {
    renderCard({}, { planMax: '1Gi', planMaxBytes: GI, overPlan: true });
    const warning = await screen.findByTestId('disk-over-plan');
    expect(warning).toHaveTextContent(/next deploy/i);
    expect(warning).toHaveTextContent('1Gi');
    expect(warning).toHaveTextContent(/stopped/i);
  });

  test('grows the disk to a size in Mi', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { id: 'app-1', disk } } as never);
    const user = renderCard(
      { disk: { mountPath: '/data', size: '500Mi' } },
      { size: '500Mi', sizeBytes: 500 * MI },
    );
    await enterSize(user, '800', 'Mi');
    await user.click(screen.getByTestId('disk-resize'));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith(DISK_URL, { size: '800Mi' }));
  });

  test('a running container cannot make its disk smaller', async () => {
    const user = renderCard();
    await enterSize(user, '2', 'Gi');
    expect(screen.getByTestId('disk-resize')).toBeDisabled();
    expect(screen.getByTestId('disk-resize-hint')).toHaveTextContent(/stop the container/i);
  });

  test('a stopped container can lower its disk, never below what it holds', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { id: 'app-1', disk } } as never);
    const user = renderCard({ status: 'PAUSED' });
    await screen.findByTestId('disk-usage');
    expect(screen.getByTestId('disk-resize-hint')).toHaveTextContent('120Mi');
    await enterSize(user, '512', 'Mi');
    await user.click(screen.getByTestId('disk-resize'));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith(DISK_URL, { size: '512Mi' }));
  });

  test('the same size, or less than 64Mi, is not offered', async () => {
    const user = renderCard({ status: 'PAUSED' });
    await enterSize(user, '5', 'Gi');
    expect(screen.getByTestId('disk-resize')).toBeDisabled();
    await enterSize(user, '32', 'Mi');
    expect(screen.getByTestId('disk-resize')).toBeDisabled();
  });

  test('a refused resize shows the server message verbatim', async () => {
    vi.mocked(api.post).mockRejectedValue(
      refusal(409, 'the platform storage budget has no room for 10Gi'),
    );
    const user = renderCard();
    await enterSize(user, '10', 'Gi');
    await user.click(screen.getByTestId('disk-resize'));
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'the platform storage budget has no room for 10Gi',
    );
  });
});
