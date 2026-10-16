import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { SiteDomains } from './SiteDomains';
import { api } from '../../api/client';
import type { SiteDomain } from '../../api/siteDomains';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), patch: vi.fn(), delete: vi.fn() },
}));

const pending: SiteDomain = {
  id: 'dom-1',
  siteId: 'site-1',
  projectId: 'proj-1',
  hostname: 'www.example.com',
  validation: 'txt',
  status: 'PENDING',
  hostnameStatus: 'pending',
  certificateStatus: 'pending_validation',
  records: [
    { type: 'CNAME', name: 'www.example.com', value: 'cname.example.app', purpose: 'routing' },
    {
      type: 'TXT',
      name: '_excalibase-challenge.www.example.com',
      value: 'excalibase-verification=tok-1',
      purpose: 'verification',
    },
    {
      type: 'TXT',
      name: '_acme-challenge.www.example.com',
      value: 'dcv-1',
      purpose: 'certificate',
    },
  ],
  errors: ['custom hostname does not CNAME to this zone.'],
  createdBy: 'u1',
  createdAt: '2026-10-17T10:00:00Z',
  updatedAt: '2026-10-17T10:00:00Z',
  checkedAt: '2026-10-17T10:00:00Z',
};
const active: SiteDomain = {
  ...pending,
  id: 'dom-2',
  hostname: 'shop.example.com',
  status: 'ACTIVE',
  hostnameStatus: 'active',
  certificateStatus: 'active',
  records: [
    { type: 'CNAME', name: 'shop.example.com', value: 'cname.example.app', purpose: 'routing' },
  ],
  errors: [],
};

function renderDomains({
  canDevelop = true,
  enabled = true,
  domains = [pending, active],
}: { canDevelop?: boolean; enabled?: boolean; domains?: SiteDomain[] } = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/config')
      return Promise.resolve({
        data: { deploymentMode: 'cloud', sites: true, siteCustomDomains: enabled },
      } as never);
    if (url === '/projects/proj-1/sites/site-1/domains')
      return Promise.resolve({ data: domains } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <SiteDomains projectId="proj-1" siteId="site-1" canDevelop={canDevelop} />
    </QueryClientProvider>,
  );
}

describe('SiteDomains', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.post).mockReset();
    vi.mocked(api.delete).mockReset();
  });

  test('lists each domain with its status and the DNS records still to add', async () => {
    renderDomains();
    const row = await screen.findByTestId('site-domain-www.example.com');
    expect(within(row).getByTestId('site-domain-status-dom-1')).toHaveTextContent(/waiting/i);
    const records = within(row).getByTestId('site-domain-records-dom-1');
    expect(records).toHaveTextContent('CNAME');
    expect(records).toHaveTextContent('cname.example.app');
    expect(records).toHaveTextContent('_acme-challenge.www.example.com');
    expect(records).toHaveTextContent('dcv-1');
    expect(records).toHaveTextContent('excalibase-verification=tok-1');
    expect(records).toHaveTextContent(/proves you control the domain/i);
    expect(row).toHaveTextContent('custom hostname does not CNAME to this zone.');
    const live = screen.getByTestId('site-domain-shop.example.com');
    expect(within(live).getByTestId('site-domain-status-dom-2')).toHaveTextContent('Live');
    expect(within(live).getByRole('link', { name: 'https://shop.example.com' })).toHaveAttribute(
      'href',
      'https://shop.example.com',
    );
  });

  test('adds a domain, with TXT validation when asked', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: pending } as never);
    renderDomains({ domains: [] });
    await user.type(await screen.findByTestId('site-domain-input'), ' www.example.com ');
    await user.selectOptions(screen.getByTestId('site-domain-validation'), 'txt');
    await user.click(screen.getByTestId('site-domain-add'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/projects/proj-1/sites/site-1/domains', {
        hostname: 'www.example.com',
        validation: 'txt',
      }),
    );
  });

  test('shows why a domain was refused', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({
      response: { status: 409, data: { error: 'that domain is already added to a site' } },
    });
    renderDomains({ domains: [] });
    await user.type(await screen.findByTestId('site-domain-input'), 'www.example.com');
    await user.click(screen.getByTestId('site-domain-add'));
    expect(await screen.findByRole('alert')).toHaveTextContent('already added to a site');
  });

  test('checks a waiting domain now', async () => {
    const user = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({ data: { ...pending, status: 'ACTIVE' } } as never);
    renderDomains();
    await user.click(await screen.findByTestId('site-domain-check-dom-1'));
    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/projects/proj-1/sites/site-1/domains/dom-1/refresh'),
    );
    expect(screen.queryByTestId('site-domain-check-dom-2')).not.toBeInTheDocument();
  });

  test('removes a domain only after confirming', async () => {
    const user = userEvent.setup();
    vi.mocked(api.delete).mockResolvedValue({ data: undefined } as never);
    renderDomains();
    await user.click(await screen.findByTestId('site-domain-remove-dom-2'));
    expect(api.delete).not.toHaveBeenCalled();
    await user.click(screen.getByTestId('modal-confirm'));
    await waitFor(() =>
      expect(api.delete).toHaveBeenCalledWith('/projects/proj-1/sites/site-1/domains/dom-2'),
    );
  });

  test('a member who may not change the site only reads', async () => {
    renderDomains({ canDevelop: false });
    await screen.findByTestId('site-domain-www.example.com');
    expect(screen.queryByTestId('site-domain-input')).not.toBeInTheDocument();
    expect(screen.queryByTestId('site-domain-check-dom-1')).not.toBeInTheDocument();
    expect(screen.queryByTestId('site-domain-remove-dom-1')).not.toBeInTheDocument();
  });

  test('an install without custom domains says so and calls nothing', async () => {
    renderDomains({ enabled: false });
    expect(await screen.findByTestId('site-domains-unavailable')).toHaveTextContent(
      /not set up on this install/i,
    );
    expect(api.get).not.toHaveBeenCalledWith('/projects/proj-1/sites/site-1/domains');
  });
});
