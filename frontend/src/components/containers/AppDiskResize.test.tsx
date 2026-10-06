import { describe, test, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AppDiskResize } from './AppDiskResize';
import { api } from '../../api/client';
import type { App, AppDisk } from '../../api/apps';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const conflict = {
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: 'the disk is being resized already', status: 409 } },
};

const app: App & { disk: AppDisk } = {
  id: 'app-1',
  projectId: 'proj-1',
  name: 'web',
  image: 'nginx:1.27',
  env: [],
  port: 8080,
  replicas: 1,
  disk: { mountPath: '/data', size: '5Gi' },
  tier: 'STANDARD',
  status: 'PAUSED',
  version: 3,
  createdAt: '2026-09-20T10:00:00Z',
  updatedAt: '2026-09-20T10:00:00Z',
};

function mount(overrides: Partial<App & { disk: AppDisk }> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <AppDiskResize app={{ ...app, ...overrides }} usedBytes={1024 ** 3} />
    </QueryClientProvider>,
  );
}

const setSize = (amount: string) =>
  fireEvent.change(screen.getByTestId('disk-resize-size'), { target: { value: amount } });

describe('AppDiskResize', () => {
  beforeEach(() => vi.clearAllMocks());

  test('an empty, zero, fractional or unchanged size cannot be submitted', () => {
    mount();
    expect(screen.getByTestId('disk-resize')).toBeDisabled();
    setSize('');
    expect(screen.getByTestId('disk-resize')).toBeDisabled();
    setSize('0');
    expect(screen.getByTestId('disk-resize')).toBeDisabled();
    setSize('1.5');
    expect(screen.getByTestId('disk-resize')).toBeDisabled();
  });

  test('a running container cannot be made smaller and says why', () => {
    mount({ status: 'ACTIVE' });
    setSize('2');
    expect(screen.getByTestId('disk-resize')).toBeDisabled();
    expect(screen.getByTestId('disk-resize-hint')).toHaveTextContent(/stop the container/i);
  });

  test('a stopped container offers to shrink down to what the disk holds', () => {
    mount();
    setSize('2');
    expect(screen.getByTestId('disk-resize')).toHaveTextContent('Make disk smaller');
    expect(screen.getByTestId('disk-resize-hint')).toHaveTextContent('1Gi');
  });

  test('switching units sends the size in that unit', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { id: 'app-1', disk: app.disk } } as never);
    mount();
    setSize('8192');
    fireEvent.change(screen.getByTestId('disk-resize-unit'), { target: { value: 'Mi' } });
    fireEvent.click(screen.getByTestId('disk-resize'));
    await vi.waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/projects/proj-1/apps/app-1/disk', { size: '8192Mi' }),
    );
  });

  test('a refusal shows the server reason, not the status code', async () => {
    vi.mocked(api.post).mockRejectedValue(conflict);
    mount();
    setSize('10');
    fireEvent.click(screen.getByTestId('disk-resize'));
    expect(await screen.findByRole('alert')).toHaveTextContent('the disk is being resized already');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('two quick clicks send one resize', async () => {
    vi.mocked(api.post).mockReturnValue(new Promise(() => {}));
    mount();
    setSize('10');
    fireEvent.click(screen.getByTestId('disk-resize'));
    fireEvent.click(screen.getByTestId('disk-resize'));
    await vi.waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(api.post).toHaveBeenCalledTimes(1);
  });
});
