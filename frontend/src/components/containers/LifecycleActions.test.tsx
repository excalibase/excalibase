import { describe, test, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { LifecycleActions } from './LifecycleActions';
import { api } from '../../api/client';
import type { App } from '../../api/apps';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const navigate = vi.fn();
vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual<typeof import('react-router-dom')>('react-router-dom');
  return { ...actual, useNavigate: () => navigate };
});

const conflict = {
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: 'a deploy is rolling out', status: 409 } },
};

const app: App = {
  id: 'app-1',
  projectId: 'proj-1',
  name: 'web',
  image: 'nginx:1.27',
  env: [],
  port: 8080,
  replicas: 1,
  tier: 'STANDARD',
  status: 'ACTIVE',
  version: 3,
  createdAt: '2026-09-20T10:00:00Z',
  updatedAt: '2026-09-20T10:00:00Z',
};

const accepted = { data: { id: 'app-1', status: 'PAUSING', acceptedAt: '2026-10-01T10:00:00Z' } };

function mount(overrides: Partial<App> = {}) {
  const onError = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <LifecycleActions app={{ ...app, ...overrides }} deployed onError={onError} followMs={10} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return onError;
}

describe('LifecycleActions', () => {
  beforeEach(() => vi.clearAllMocks());

  test('pause follows the app until it is paused', async () => {
    vi.mocked(api.post).mockResolvedValue(accepted as never);
    vi.mocked(api.get).mockResolvedValue({ data: { ...app, status: 'PAUSED' } } as never);
    const onError = mount();
    fireEvent.click(screen.getByTestId('pause-button'));
    await vi.waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/projects/proj-1/apps/app-1/pause', undefined, {
        headers: { Prefer: 'respond-async' },
      }),
    );
    await vi.waitFor(() => expect(api.get).toHaveBeenCalled());
    await vi.waitFor(() => expect(screen.queryByTestId('lifecycle-pending')).not.toBeInTheDocument());
    expect(onError).toHaveBeenCalledWith(null);
    expect(onError).not.toHaveBeenCalledWith(expect.anything());
  });

  test('a paused app with replicas can be resumed', async () => {
    vi.mocked(api.post).mockResolvedValue(accepted as never);
    vi.mocked(api.get).mockReturnValue(new Promise(() => {}));
    mount({ status: 'PAUSED' });
    expect(screen.queryByTestId('pause-button')).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId('resume-button'));
    expect(await screen.findByTestId('lifecycle-pending')).toHaveTextContent('Resuming');
  });

  test('a refused pause hands the server error to the page', async () => {
    vi.mocked(api.post).mockRejectedValue(conflict);
    const onError = mount();
    fireEvent.click(screen.getByTestId('pause-button'));
    await vi.waitFor(() => expect(onError.mock.calls.map(([err]) => err)).toContain(conflict));
  });

  test('a pause the cluster could not finish reports its reason', async () => {
    vi.mocked(api.post).mockResolvedValue(accepted as never);
    vi.mocked(api.get).mockResolvedValue({
      data: { ...app, lifecycleFailure: { operation: 'pause', at: '2026-10-01T10:00:05Z', reason: 'node drained' } },
    } as never);
    const onError = mount();
    fireEvent.click(screen.getByTestId('pause-button'));
    await vi.waitFor(() =>
      expect(onError).toHaveBeenCalledWith(expect.objectContaining({ message: expect.stringContaining('node drained') })),
    );
  });

  test('two quick pause clicks send one request', async () => {
    vi.mocked(api.post).mockReturnValue(new Promise(() => {}));
    mount();
    fireEvent.click(screen.getByTestId('pause-button'));
    fireEvent.click(screen.getByTestId('pause-button'));
    await vi.waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(api.post).toHaveBeenCalledTimes(1);
  });

  test('deleting a container with a disk needs its name typed', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: { ...accepted.data, status: 'DELETING' } } as never);
    vi.mocked(api.get).mockRejectedValue({ response: { status: 404 } });
    mount({ disk: { mountPath: '/data', size: '5Gi' } });
    fireEvent.click(screen.getByTestId('delete-button'));
    expect(screen.getByTestId('delete-confirm-text')).toHaveTextContent(/every file on the disk is erased/);
    expect(screen.getByTestId('delete-confirm')).toBeDisabled();
    fireEvent.change(screen.getByTestId('delete-confirm-name'), { target: { value: 'wbe' } });
    expect(screen.getByTestId('delete-confirm')).toBeDisabled();
    fireEvent.change(screen.getByTestId('delete-confirm-name'), { target: { value: 'web' } });
    fireEvent.click(screen.getByTestId('delete-confirm'));
    await vi.waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith('/projects/proj-1/apps/app-1', {
        data: { confirmDeleteDisk: true },
        headers: { Prefer: 'respond-async' },
      }),
    );
    await vi.waitFor(() => expect(navigate).toHaveBeenCalledWith('/project/proj-1/containers'));
  });

  test('cancel closes the delete confirmation without a request', () => {
    mount();
    fireEvent.click(screen.getByTestId('delete-button'));
    fireEvent.click(screen.getByTestId('delete-cancel'));
    expect(screen.getByTestId('delete-button')).toBeInTheDocument();
    expect(api.delete).not.toHaveBeenCalled();
  });

  test('a refused delete hands the server error to the page', async () => {
    vi.mocked(api.delete).mockRejectedValue(conflict);
    const onError = mount();
    fireEvent.click(screen.getByTestId('delete-button'));
    fireEvent.click(screen.getByTestId('delete-confirm'));
    await vi.waitFor(() => expect(onError.mock.calls.map(([err]) => err)).toContain(conflict));
  });

  test('two quick delete confirms send one request', async () => {
    vi.mocked(api.delete).mockReturnValue(new Promise(() => {}));
    mount();
    fireEvent.click(screen.getByTestId('delete-button'));
    fireEvent.click(screen.getByTestId('delete-confirm'));
    fireEvent.click(screen.getByTestId('delete-confirm'));
    await vi.waitFor(() => expect(api.delete).toHaveBeenCalled());
    expect(api.delete).toHaveBeenCalledTimes(1);
  });
});
