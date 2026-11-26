import { describe, test, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { RegisterPage } from './RegisterPage';
import { CreditTermsBox } from '../components/auth/CreditTermsBox';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

const PASSWORD = ['Sign', 'Up', '1', '!'].join('');

function serve(config: object) {
  vi.mocked(api.get).mockImplementation(async (url: string) => {
    if (url === '/config') return { data: { deploymentMode: 'cloud', ...config } } as never;
    return { data: { providers: ['github'] } } as never;
  });
  vi.mocked(api.post).mockResolvedValue({ data: { status: 'verification_required', email: 'a@x.test' } } as never);
}

function renderRegister() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter><RegisterPage /></MemoryRouter>
    </QueryClientProvider>,
  );
}

async function fillAndSubmit() {
  const u = userEvent.setup();
  await u.type(screen.getByLabelText('Username'), 'alice');
  await u.type(screen.getByLabelText('Email'), 'a@x.test');
  await u.type(screen.getByLabelText('Password'), PASSWORD);
  await u.type(screen.getByLabelText('Confirm password'), PASSWORD);
  return u;
}

describe('sign-up credit terms', () => {
  beforeEach(() => vi.clearAllMocks());

  test('no box while credits are off', async () => {
    serve({});
    renderRegister();
    await screen.findByText(/continue with github/i);
    expect(screen.queryByRole('checkbox')).toBeNull();
  });

  test('a ticked box sends the version to the register call and the provider link', async () => {
    serve({ credits: { termsVersion: 'v7', termsUrl: 'https://x.test/terms' } });
    renderRegister();
    const u = await fillAndSubmit();
    await u.click(await screen.findByRole('checkbox'));
    expect(screen.getByRole('link', { name: /continue with github/i }).getAttribute('href')).toContain('creditTerms=v7');
    await u.click(screen.getByRole('button', { name: /create account/i }));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith('/auth/register', expect.objectContaining({ creditTermsVersion: 'v7' })));
  });

  test('an empty box still signs up, with no terms sent and none on the provider link', async () => {
    serve({ credits: { termsVersion: 'v7' } });
    renderRegister();
    const u = await fillAndSubmit();
    await screen.findByRole('checkbox');
    expect(screen.getByRole('link', { name: /continue with github/i }).getAttribute('href')).not.toContain('creditTerms');
    await u.click(screen.getByRole('button', { name: /create account/i }));
    await waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(vi.mocked(api.post).mock.calls[0][1]).not.toHaveProperty('creditTermsVersion');
  });
});

describe('the terms box on its own', () => {
  test('links the terms when the server gives a URL', async () => {
    serve({ credits: { termsVersion: 'v7', termsUrl: 'https://x.test/terms' } });
    const client = new QueryClient();
    render(<QueryClientProvider client={client}><CreditTermsBox accepted={false} onChange={() => {}} /></QueryClientProvider>);
    expect((await screen.findByRole('link', { name: 'credit terms' })).getAttribute('href')).toBe('https://x.test/terms');
  });
});
