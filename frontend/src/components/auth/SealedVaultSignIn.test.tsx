import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { SealedVaultSignIn } from './SealedVaultSignIn';
import { api } from '../../api/client';
import { useAuthStore } from '../../stores/auth-store';

vi.mock('../../api/client', () => ({ api: { post: vi.fn() } }));

const TEST_PASSWORD = ['Founder', '1', '!'].join('');

describe('SealedVaultSignIn', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAuthStore.getState().clearAuth();
  });

  test('signs the admin in and records the profile', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({
      data: {
        user: { id: 'u1', username: 'founder', email: 'f@example.com', role: 'platform_admin' },
      },
    } as never);
    render(<SealedVaultSignIn />);

    expect(screen.getByTestId('vault-unseal-signin-submit')).toBeDisabled();
    await user.type(screen.getByTestId('vault-unseal-signin-username'), ' founder ');
    await user.type(screen.getByTestId('vault-unseal-signin-password'), TEST_PASSWORD);
    await user.click(screen.getByTestId('vault-unseal-signin-submit'));

    await waitFor(() => expect(useAuthStore.getState().isAuthenticated).toBe(true));
    expect(api.post).toHaveBeenCalledWith('/auth/login', {
      username: 'founder',
      password: TEST_PASSWORD,
    });
  });

  test('a refused sign-in shows the reason and stays signed out', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({
      response: { status: 401, data: { error: 'invalid credentials' } },
    });
    render(<SealedVaultSignIn />);

    await user.type(screen.getByTestId('vault-unseal-signin-username'), 'founder');
    await user.type(screen.getByTestId('vault-unseal-signin-password'), TEST_PASSWORD);
    await user.click(screen.getByTestId('vault-unseal-signin-submit'));

    expect(await screen.findByRole('alert')).toHaveTextContent('invalid credentials');
    expect(useAuthStore.getState().isAuthenticated).toBe(false);
  });
});
