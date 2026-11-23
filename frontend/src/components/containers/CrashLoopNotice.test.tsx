import { describe, test, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { CrashLoopNotice } from './CrashLoopNotice';
import { api } from '../../api/client';
import type { App } from '../../api/apps';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const stopped: App = {
  id: 'app-1',
  projectId: 'proj-1',
  name: 'web',
  image: 'nginx:1.27',
  env: [],
  port: 8080,
  replicas: 1,
  tier: 'STANDARD',
  status: 'PAUSED',
  suspension: {
    kind: 'crash-loop',
    reason: 'kept crashing (11 restarts in the last hour)',
    restarts: 11,
    windowMinutes: 60,
    at: '2026-11-23T10:00:00Z',
  },
  version: 3,
  createdAt: '2026-09-20T10:00:00Z',
  updatedAt: '2026-09-20T10:00:00Z',
};

const accepted = { data: { id: 'app-1', status: 'RESUMING', acceptedAt: '2026-11-23T10:05:00Z' } };

function mount(overrides: Partial<App> = {}, canStart = true) {
  const onError = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <CrashLoopNotice app={{ ...stopped, ...overrides }} canStart={canStart} onError={onError} followMs={10} />
    </QueryClientProvider>,
  );
  return onError;
}

describe('CrashLoopNotice', () => {
  beforeEach(() => vi.clearAllMocks());

  test('says why the app was stopped and how often it crashed', () => {
    mount();
    expect(screen.getByTestId('crash-loop-text')).toHaveTextContent(
      'Stopped: kept crashing (11 restarts in the last hour)',
    );
  });

  test('shows nothing for an app nobody stopped', () => {
    mount({ suspension: undefined });
    expect(screen.queryByTestId('crash-loop-notice')).not.toBeInTheDocument();
  });

  test('shows nothing once the app runs again, whatever a stale record says', () => {
    mount({ status: 'ACTIVE' });
    expect(screen.queryByTestId('crash-loop-notice')).not.toBeInTheDocument();
  });

  test('ignores a stop of another kind', () => {
    mount({ suspension: { ...stopped.suspension!, kind: 'something-else' } });
    expect(screen.queryByTestId('crash-loop-notice')).not.toBeInTheDocument();
  });

  test('Start again resumes the app and follows it until it runs', async () => {
    vi.mocked(api.post).mockResolvedValue(accepted as never);
    vi.mocked(api.get).mockResolvedValue({ data: { ...stopped, status: 'ACTIVE', suspension: undefined } } as never);
    const onError = mount();
    fireEvent.click(screen.getByTestId('start-again-button'));
    await vi.waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/projects/proj-1/apps/app-1/resume', undefined, {
        headers: { Prefer: 'respond-async' },
      }),
    );
    await vi.waitFor(() => expect(api.get).toHaveBeenCalled());
    expect(onError).toHaveBeenCalledWith(null);
  });

  test('a refused start hands the server error to the page', async () => {
    const refused = { message: 'x', response: { status: 409, data: { error: 'busy', status: 409 } } };
    vi.mocked(api.post).mockRejectedValue(refused);
    const onError = mount();
    fireEvent.click(screen.getByTestId('start-again-button'));
    await vi.waitFor(() => expect(onError).toHaveBeenCalled());
    expect(onError.mock.calls[0][0]).toBe(refused);
  });

  test('a viewer sees the reason but no button', () => {
    mount({}, false);
    expect(screen.getByTestId('crash-loop-notice')).toBeInTheDocument();
    expect(screen.queryByTestId('start-again-button')).not.toBeInTheDocument();
  });
});
