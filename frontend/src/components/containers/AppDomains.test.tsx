import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AppDomains } from './AppDomains';
import { api } from '../../api/client';
import type { AppDomain } from '../../api/appDomains';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const BASE = '/projects/p1/apps/a1/domains';
const pending: AppDomain = {
  id: 'd1',
  hostname: 'shop.example.com',
  status: 'pending',
  consecutiveFailures: 0,
  cnameTarget: 'web-p1.apps.example.com',
};

function renderDomains(domains: AppDomain[]) {
  const state = { domains: [...domains] };
  vi.mocked(api.get).mockImplementation(() => Promise.resolve({ data: state.domains } as never));
  vi.mocked(api.post).mockImplementation((url: string, body?: unknown) => {
    if (url === `${BASE}/`) {
      state.domains = [
        ...state.domains,
        { ...pending, id: 'd2', hostname: (body as { hostname: string }).hostname },
      ];
      return Promise.resolve({ data: state.domains.at(-1) } as never);
    }
    state.domains = state.domains.map((d) =>
      url.includes(d.id) ? { ...d, status: 'issuing' } : d,
    );
    return Promise.resolve({ data: {} } as never);
  });
  vi.mocked(api.delete).mockImplementation((url: string) => {
    state.domains = state.domains.filter((d) => !url.endsWith(d.id));
    return Promise.resolve({ status: 204 } as never);
  });
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  render(
    <QueryClientProvider client={client}>
      <AppDomains projectId="p1" appId="a1" />
    </QueryClientProvider>,
  );
  return userEvent.setup();
}

describe('AppDomains', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.post).mockReset();
    vi.mocked(api.delete).mockReset();
  });

  test('a pending domain says where to point its CNAME', async () => {
    renderDomains([pending]);
    const row = await screen.findByTestId('domain-row-shop.example.com');
    expect(row).toHaveTextContent('web-p1.apps.example.com');
    expect(row).toHaveTextContent(/waiting for dns/i);
  });

  test('the domain field shows a subdomain example and refuses a bare domain in words', async () => {
    renderDomains([]);
    expect(await screen.findByTestId('domain-input')).toHaveAttribute(
      'placeholder',
      'shop.example.com',
    );
    expect(
      screen.getByText('A subdomain such as shop.example.com, not a bare domain like example.com.'),
    ).toBeInTheDocument();
  });

  test('adding and verifying a domain', async () => {
    const user = renderDomains([]);
    await user.type(await screen.findByTestId('domain-input'), 'www.example.com');
    await user.click(screen.getByTestId('domain-add'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(`${BASE}/`, { hostname: 'www.example.com' }),
    );
    const row = await screen.findByTestId('domain-row-www.example.com');
    await user.click(within(row).getByTestId('domain-verify-d2'));
    await waitFor(() => expect(api.post).toHaveBeenCalledWith(`${BASE}/d2/verify`));
    await waitFor(() =>
      expect(screen.getByTestId('domain-row-www.example.com')).toHaveTextContent(/issuing/i),
    );
  });

  test('a failed check says why', async () => {
    renderDomains([
      { ...pending, status: 'detached', failureReason: 'the CNAME points at elsewhere.net' },
    ]);
    expect(await screen.findByTestId('domain-row-shop.example.com')).toHaveTextContent(
      'elsewhere.net',
    );
  });

  test('a refusal is shown', async () => {
    const user = renderDomains([]);
    vi.mocked(api.post).mockRejectedValueOnce({
      response: { data: { error: 'invalid custom domain: apex' } },
    });
    await user.type(await screen.findByTestId('domain-input'), 'example.com');
    await user.click(screen.getByTestId('domain-add'));
    expect(await screen.findByRole('alert')).toHaveTextContent(/apex/);
  });

  test('removing a domain', async () => {
    const user = renderDomains([pending]);
    const row = await screen.findByTestId('domain-row-shop.example.com');
    await user.click(within(row).getByTestId('domain-remove-d1'));
    await waitFor(() => expect(api.delete).toHaveBeenCalledWith(`${BASE}/d1`));
  });
});
