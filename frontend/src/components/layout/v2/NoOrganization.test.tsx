import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import { NoOrganization, inviteToken } from './NoOrganization';
import { api } from '../../../api/client';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

function Where() {
  return <p data-testid="where">{useLocation().pathname}</p>;
}

function renderPage() {
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={['/']}>
        <Routes>
          <Route path="/" element={<NoOrganization />} />
          <Route path="/orgs" element={<Where />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe('inviteToken', () => {
  test('takes the code from an invite link or a bare code', () => {
    expect(inviteToken('https://app.example.test/login?invite=abc123')).toBe('abc123');
    expect(inviteToken('  abc123  ')).toBe('abc123');
    expect(inviteToken('https://app.example.test/register?x=1&invite=a%2Bb')).toBe('a+b');
    expect(inviteToken('   ')).toBe('');
  });

  test('a link with no invite gives no code', () => {
    expect(inviteToken('https://app.example.test/login')).toBe('');
  });

  test('takes the code from the relative link the server sends', () => {
    expect(inviteToken('/register?invite=rel-1')).toBe('rel-1');
    expect(inviteToken('/login?invite=a%2Bb&x=1')).toBe('a+b');
  });
});

describe('NoOrganization', () => {
  beforeEach(() => vi.clearAllMocks());

  test('offers to create an organization', () => {
    renderPage();
    expect(screen.getByRole('link', { name: /create an organization/i })).toHaveAttribute('href', '/orgs?new=1');
  });

  test('accepting needs a code', () => {
    renderPage();
    expect(screen.getByRole('button', { name: /accept invite/i })).toBeDisabled();
  });

  test('a pasted invite link is accepted with its code, then the org list opens', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
    renderPage();
    await userEvent.type(screen.getByLabelText(/invite link or code/i), 'https://app.example.test/login?invite=tok-9');
    await userEvent.click(screen.getByRole('button', { name: /accept invite/i }));
    expect(api.post).toHaveBeenCalledWith('/orgs/invites/accept', { token: 'tok-9' });
    expect(await screen.findByTestId('where')).toHaveTextContent('/orgs');
  });

  test('a refused invite shows the server reason and stays on the page', async () => {
    vi.mocked(api.post).mockRejectedValue({ response: { data: { error: 'Invite expired' } } });
    renderPage();
    await userEvent.type(screen.getByLabelText(/invite link or code/i), 'old');
    await userEvent.click(screen.getByRole('button', { name: /accept invite/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Invite expired');
    expect(screen.queryByTestId('where')).not.toBeInTheDocument();
  });

  test('a failure with no reason still says it failed', async () => {
    vi.mocked(api.post).mockRejectedValue(new Error('network'));
    renderPage();
    await userEvent.type(screen.getByLabelText(/invite link or code/i), 'x');
    await userEvent.click(screen.getByRole('button', { name: /accept invite/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/could not accept the invite/i);
  });
});
