import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { FunctionEgressCard } from './FunctionEgressCard';
import { api } from '../api/client';

vi.mock('../api/client', () => ({
  api: { get: vi.fn(), put: vi.fn() },
}));

const egress = (allowedHosts: string[], defaultHosts: string[] = []) => ({
  data: { allowedHosts, defaultHosts, effectiveHosts: [...defaultHosts, ...allowedHosts] },
});

function renderCard(answer: ReturnType<typeof egress> | Error) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url !== '/projects/p-1/functions/egress') return Promise.reject(new Error(`unexpected GET ${url}`));
    return answer instanceof Error ? Promise.reject(answer) : Promise.resolve(answer as never);
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <FunctionEgressCard projectId="p-1" />
    </QueryClientProvider>,
  );
}

const refusal = (status: number, error: string) => Object.assign(new Error('refused'), { response: { status, data: { error } } });

describe('FunctionEgressCard', () => {
  beforeEach(() => {
    vi.mocked(api.get).mockReset();
    vi.mocked(api.put).mockReset();
  });

  test('lists the project hosts with their port and the installation defaults, and explains the syntax', async () => {
    renderCard(egress(['api.stripe.com:443', '*.example.com:8443'], ['hooks.slack.com:443']));
    const list = await screen.findByTestId('egress-hosts-list');
    expect(within(list).getByText('api.stripe.com:443')).toBeInTheDocument();
    expect(within(list).getByText('*.example.com:8443')).toBeInTheDocument();
    expect(screen.getByTestId('egress-default-hosts')).toHaveTextContent('hooks.slack.com:443');
    const card = screen.getByTestId('egress-card');
    expect(card).toHaveTextContent('Outbound hosts');
    expect(card).toHaveTextContent(/:443/);
    expect(card).toHaveTextContent('*.example.com');
  });

  test('says when no outside host is allowed', async () => {
    renderCard(egress([]));
    expect(await screen.findByTestId('egress-hosts-empty')).toHaveTextContent(/blocked/i);
  });

  test('adds a host by saving the whole list and shows the port the server pinned', async () => {
    vi.mocked(api.put).mockResolvedValue(egress(['api.stripe.com:443', 'example.com:443']) as never);
    renderCard(egress(['api.stripe.com:443']));
    await userEvent.type(await screen.findByLabelText('Host'), ' example.com ');
    await userEvent.click(screen.getByRole('button', { name: 'Add host' }));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/projects/p-1/functions/egress', {
        allowedHosts: ['api.stripe.com:443', 'example.com'],
      }),
    );
    expect(await within(screen.getByTestId('egress-hosts-list')).findByText('example.com:443')).toBeInTheDocument();
    expect(screen.getByLabelText('Host')).toHaveValue('');
  });

  test('an empty host is not sent', async () => {
    renderCard(egress([]));
    await screen.findByTestId('egress-hosts-empty');
    expect(screen.getByRole('button', { name: 'Add host' })).toBeDisabled();
  });

  test('removes a host', async () => {
    vi.mocked(api.put).mockResolvedValue(egress([]) as never);
    renderCard(egress(['api.stripe.com:443']));
    await userEvent.click(await screen.findByRole('button', { name: 'Remove api.stripe.com:443' }));
    await waitFor(() =>
      expect(api.put).toHaveBeenCalledWith('/projects/p-1/functions/egress', { allowedHosts: [] }),
    );
    expect(await screen.findByTestId('egress-hosts-empty')).toBeInTheDocument();
  });

  test('shows the server reason when a host is refused', async () => {
    vi.mocked(api.put).mockRejectedValue(refusal(400, 'invalid egress host: "10.0.0.1:443" is not a public address'));
    renderCard(egress([]));
    await userEvent.type(await screen.findByLabelText('Host'), '10.0.0.1');
    await userEvent.click(screen.getByRole('button', { name: 'Add host' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('is not a public address');
  });

  test('a read the server refuses shows its reason', async () => {
    renderCard(refusal(503, 'egress allowlist not configured'));
    expect(await screen.findByTestId('egress-error')).toHaveTextContent('egress allowlist not configured');
  });
});
