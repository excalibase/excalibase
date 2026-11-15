import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { PlatformLayoutSwitch } from './LayoutSwitch';
import { api } from '../../../api/client';
import { useAuthStore } from '../../../stores/auth-store';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

function renderSwitch() {
  useAuthStore.setState({ user: { id: 'u1', username: 'dev', email: 'dev@example.test', role: 'user' }, isAuthenticated: true });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route element={<PlatformLayoutSwitch />}>
            <Route path="/" element={<p>page</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

function answer(config: object) {
  vi.mocked(api.get).mockImplementation((url: string) =>
    Promise.resolve({ data: url === '/config' ? config : [] }) as never);
}

describe('studio_v2 flag gate', () => {
  beforeEach(() => vi.clearAllMocks());

  test('flag off: the current layout, no new shell', async () => {
    answer({ deploymentMode: 'cloud', features: { studio_v2: false } });
    renderSwitch();
    expect(await screen.findByTestId('platform-nav')).toBeInTheDocument();
    expect(screen.queryByTestId('studio-shell')).not.toBeInTheDocument();
  });

  test('the server reporting no features is the flag off', async () => {
    answer({ deploymentMode: 'cloud' });
    renderSwitch();
    expect(await screen.findByTestId('platform-nav')).toBeInTheDocument();
    expect(screen.queryByTestId('studio-shell')).not.toBeInTheDocument();
  });

  test('flag on: the new shell, not the current navigation', async () => {
    answer({ deploymentMode: 'cloud', features: { studio_v2: true } });
    renderSwitch();
    expect(await screen.findByTestId('studio-shell')).toBeInTheDocument();
    expect(screen.queryByTestId('platform-nav')).not.toBeInTheDocument();
    expect(screen.getByText('page')).toBeInTheDocument();
  });

  test('until the server answers, neither layout is guessed', () => {
    vi.mocked(api.get).mockReturnValue(new Promise(() => {}) as never);
    renderSwitch();
    expect(screen.queryByTestId('studio-shell')).not.toBeInTheDocument();
    expect(screen.queryByTestId('platform-nav')).not.toBeInTheDocument();
  });
});
