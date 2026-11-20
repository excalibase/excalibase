import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ResetPasswordButton } from './ResetPasswordButton';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));
vi.mock('../hooks/useProjectRole', () => ({ useProjectRole: () => ({ canAdmin: true, canDevelop: true, role: 'admin', isLoading: false }) }));

const GENERATED = 'Gen3rated-Pass-9x2Kq';
const CHOSEN = 'my chosen passphrase here';

function mockApi(flag = true) {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/config') return { data: { deploymentMode: 'cloud', features: { onetimecredentials: flag } } } as never;
    if (url === '/provision/p-1/credentials') {
      return { data: { projectId: 'p-1', host: 'h', port: 5432, databaseName: 'appdb', username: 'app', sslMode: 'require', connectionUrl: 'postgresql://app:<password>@h:5432/appdb' } } as never;
    }
    return { data: [] } as never;
  });
  vi.mocked(api.post).mockImplementation(async (_url: string, body?: unknown) => ({
    data: { projectId: 'p-1', password: (body as { password?: string } | undefined)?.password ?? GENERATED },
  }) as never);
}

function renderButton() {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
      <ResetPasswordButton projectId="p-1" />
    </QueryClientProvider>,
  );
}

describe('ResetPasswordButton', () => {
  beforeEach(() => { vi.clearAllMocks(); mockApi(); });

  test('is absent while the one-time credentials flag is off', async () => {
    mockApi(false);
    renderButton();
    await waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.queryByRole('button', { name: /reset password/i })).not.toBeInTheDocument();
  });

  test('a blank reset sends no body and shows the generated password once', async () => {
    const user = userEvent.setup();
    renderButton();
    await user.click(await screen.findByRole('button', { name: /reset password/i }));
    await user.click(screen.getByRole('button', { name: /^reset$/i }));
    expect(api.post).toHaveBeenCalledWith('/provision/p-1/credentials/rotate');
    expect(await screen.findByTestId('otp-password')).toHaveTextContent(GENERATED);
    await user.click(screen.getByRole('button', { name: /i've saved it/i }));
    expect(document.body.innerHTML).not.toContain(GENERATED);
  });

  test('a chosen password is sent and shown back', async () => {
    const user = userEvent.setup();
    renderButton();
    await user.click(await screen.findByRole('button', { name: /reset password/i }));
    await user.type(screen.getByLabelText(/new password/i), CHOSEN);
    await user.click(screen.getByRole('button', { name: /^reset$/i }));
    expect(api.post).toHaveBeenCalledWith('/provision/p-1/credentials/rotate', { password: CHOSEN });
    expect(await screen.findByTestId('otp-password')).toHaveTextContent(CHOSEN);
  });

  test('a weak password is refused before anything is sent', async () => {
    const user = userEvent.setup();
    renderButton();
    await user.click(await screen.findByRole('button', { name: /reset password/i }));
    await user.type(screen.getByLabelText(/new password/i), 'short');
    await user.click(screen.getByRole('button', { name: /^reset$/i }));
    expect(screen.getByRole('alert')).toHaveTextContent(/at least 16/i);
    expect(api.post).not.toHaveBeenCalled();
  });

  test('the server refusal is shown', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({ message: 'x', response: { status: 400, data: { error: 'password is too weak' } } });
    renderButton();
    await user.click(await screen.findByRole('button', { name: /reset password/i }));
    await user.click(screen.getByRole('button', { name: /^reset$/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent('password is too weak');
  });
});
