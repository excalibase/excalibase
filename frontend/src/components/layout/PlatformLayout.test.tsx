import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { PlatformLayout } from './PlatformLayout';
import { api } from '../../api/client';
import { useAuthStore } from '../../stores/auth-store';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

function renderLayout(role: string) {
  useAuthStore.setState({ user: { id: 'u1', username: 'dev', email: 'dev@example.test', role }, isAuthenticated: true });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/account/tokens']}>
        <Routes>
          <Route element={<PlatformLayout />}>
            <Route path="/account/tokens" element={<p>tokens page</p>} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('PlatformLayout', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: { deploymentMode: 'cloud' } } as never);
  });

  test('every signed-in user reaches their access tokens from the navigation', () => {
    renderLayout('user');
    const link = within(screen.getByTestId('platform-nav')).getByRole('link', { name: /access tokens/i });
    expect(link).toHaveAttribute('href', '/account/tokens');
    expect(screen.getByText('tokens page')).toBeInTheDocument();
  });

  test('the account name links to account settings', () => {
    renderLayout('user');
    expect(screen.getByRole('link', { name: 'dev' })).toHaveAttribute('href', '/account/tokens');
  });

  test('platform admin entry stays hidden from ordinary users', () => {
    renderLayout('user');
    expect(screen.queryByRole('link', { name: /platform admin/i })).not.toBeInTheDocument();
  });
});
