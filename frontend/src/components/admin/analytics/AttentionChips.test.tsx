import { beforeEach, describe, expect, test, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { api } from '../../../api/client';
import { AttentionChips } from './AttentionChips';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn() } }));

function renderChips(stillActive: number, features: Record<string, boolean>, onGo = vi.fn()) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config') return Promise.resolve({ data: { deploymentMode: 'cloud', features } } as never);
    return Promise.resolve({ data: { refusedStillActive: stillActive } } as never);
  });
  render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><AttentionChips onGo={onGo} /></QueryClientProvider>);
  return onGo;
}

describe('AttentionChips', () => {
  beforeEach(() => vi.clearAllMocks());

  test('names each problem and jumps to its tab', async () => {
    const onGo = renderChips(98, {});
    await userEvent.click(await screen.findByRole('button', { name: /98 accounts had a deploy refused/ }));
    expect(onGo).toHaveBeenLastCalledWith('analytics-abuse');
    await userEvent.click(screen.getByRole('button', { name: 'Request metrics are off' }));
    expect(onGo).toHaveBeenLastCalledWith('analytics-traffic');
    await userEvent.click(screen.getByRole('button', { name: /Storage numbers are not collected/ }));
    expect(onGo).toHaveBeenLastCalledWith('analytics-storage');
  });

  test('drops the abuse chip when nobody is refused-but-active and the traffic chip when metrics are on', async () => {
    renderChips(0, { requestmetrics: true });
    await screen.findByRole('button', { name: /Storage numbers/ });
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/admin/analytics/abuse'));
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Request metrics are off' })).toBeNull());
    expect(screen.queryByRole('button', { name: /deploy refused/ })).toBeNull();
  });
});
