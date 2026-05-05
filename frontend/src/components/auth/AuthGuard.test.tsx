import { describe, test, expect, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { AuthGuard } from './AuthGuard';
import { useAuthStore } from '../../stores/auth-store';

function renderWithRoutes(initialEntries: string[]) {
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <Routes>
        <Route element={<AuthGuard />}>
          <Route path="/" element={<div data-testid="protected">PROTECTED</div>} />
        </Route>
        <Route path="/login" element={<div data-testid="login">LOGIN</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe('AuthGuard', () => {
  beforeEach(() => {
    // Reset store between tests
    useAuthStore.getState().clearAuth();
  });

  test('redirects unauthenticated users to /login', () => {
    renderWithRoutes(['/']);
    expect(screen.getByTestId('login')).toBeInTheDocument();
    expect(screen.queryByTestId('protected')).not.toBeInTheDocument();
  });

  test('lets authenticated users through to protected routes', () => {
    useAuthStore.getState().setAuth(
      {
        id: '1',
        username: 'admin',
        email: 'a@b.c',
        role: 'platform_admin',
      },
      { legacyToken: 'test-token' },
    );
    renderWithRoutes(['/']);
    expect(screen.getByTestId('protected')).toBeInTheDocument();
  });
});
