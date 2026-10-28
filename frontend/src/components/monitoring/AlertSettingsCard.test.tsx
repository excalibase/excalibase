import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AlertSettingsCard } from './AlertSettingsCard';
import { api } from '../../api/client';
import { alertSettings } from '../../test/monitoringFixtures';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), put: vi.fn(), post: vi.fn() } }));

const refusal = (error: string) => ({
  message: 'Request failed with status code 400',
  response: { status: 400, data: { error } },
});

function renderCard(canMutate = true) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <AlertSettingsCard canMutate={canMutate} />
    </QueryClientProvider>,
  );
}

const savedBody = (put: unknown) => (put as [string, Record<string, unknown>])[1];

describe('AlertSettingsCard', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ data: alertSettings() } as never);
    vi.mocked(api.put).mockImplementation(
      async (_url: string, body: unknown) =>
        ({ data: { ...alertSettings(), ...(body as object) } }) as never,
    );
  });

  test('shows the saved settings and the webhook as set', async () => {
    renderCard();
    expect(await screen.findByLabelText('CPU %')).toHaveValue('90');
    expect(screen.getByLabelText('Disk %')).toHaveValue('');
    expect(screen.getByLabelText('Email recipients')).toHaveValue('ops@example.test');
    expect(screen.getByRole('switch', { name: 'Alerts enabled' })).toHaveAttribute(
      'aria-checked',
      'true',
    );
    expect(screen.getByTestId('webhook-state')).toHaveTextContent(
      'https://hooks.example.test/…abcd (set)',
    );
  });

  test('save keeps the webhook it was not told to change', async () => {
    renderCard();
    const disk = await screen.findByLabelText('Disk %');
    await userEvent.type(disk, '80');
    await userEvent.clear(screen.getByLabelText('CPU %'));
    await userEvent.clear(screen.getByLabelText('Email recipients'));
    await userEvent.type(screen.getByLabelText('Email recipients'), 'a@x.test{enter}b@x.test');
    await userEvent.click(screen.getByRole('switch', { name: 'Alerts enabled' }));
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1));
    const [url] = vi.mocked(api.put).mock.calls[0];
    expect(url).toBe('/admin/monitoring/alert-settings');
    const body = savedBody(vi.mocked(api.put).mock.calls[0]);
    expect(body).toMatchObject({
      enabled: false,
      cpuPercent: null,
      diskPercent: 80,
      emails: ['a@x.test', 'b@x.test'],
    });
    expect(body).not.toHaveProperty('webhookUrl');
    expect(await screen.findByText('Saved.')).toBeInTheDocument();
  });

  test('a new webhook URL is sent to replace the old one', async () => {
    renderCard();
    await userEvent.type(
      await screen.findByLabelText('New webhook URL'),
      'https://hooks.example.test/new',
    );
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(api.put).toHaveBeenCalled());
    expect(savedBody(vi.mocked(api.put).mock.calls[0]).webhookUrl).toBe(
      'https://hooks.example.test/new',
    );
  });

  test('clearing the webhook sends it empty, and can be undone before saving', async () => {
    renderCard();
    await userEvent.click(await screen.findByRole('button', { name: 'Clear webhook' }));
    expect(screen.getByTestId('webhook-state')).toHaveTextContent('Removed on save');
    await userEvent.click(screen.getByRole('button', { name: 'Keep webhook' }));
    expect(screen.getByTestId('webhook-state')).toHaveTextContent('(set)');
    await userEvent.click(screen.getByRole('button', { name: 'Clear webhook' }));
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(api.put).toHaveBeenCalled());
    expect(savedBody(vi.mocked(api.put).mock.calls[0]).webhookUrl).toBe('');
  });

  test('no webhook offers nothing to clear', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: alertSettings({ webhookUrl: null, webhookSet: false }),
    } as never);
    renderCard();
    expect(await screen.findByTestId('webhook-state')).toHaveTextContent('No webhook set');
    expect(screen.queryByRole('button', { name: 'Clear webhook' })).not.toBeInTheDocument();
  });

  test("the server's refusal is shown", async () => {
    vi.mocked(api.put).mockRejectedValueOnce(refusal('webhookUrl must be an https URL'));
    renderCard();
    await userEvent.type(await screen.findByLabelText('New webhook URL'), 'http://plain');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('webhookUrl must be an https URL');
  });

  test('a value the form cannot read is refused without a call', async () => {
    renderCard();
    await userEvent.type(await screen.findByLabelText('Load 5'), 'high');
    await userEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Load 5 must be a number.');
    expect(api.put).not.toHaveBeenCalled();
  });

  test('send test shows how each channel did', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({
      data: {
        results: [
          { channel: 'email', ok: true },
          { channel: 'webhook', ok: false, error: 'connection refused' },
        ],
      },
    } as never);
    renderCard();
    await userEvent.click(await screen.findByRole('button', { name: 'Send test' }));
    const results = await screen.findByTestId('test-results');
    expect(api.post).toHaveBeenCalledWith('/admin/monitoring/alert-settings/test');
    expect(within(results).getByText('Email: sent')).toBeInTheDocument();
    expect(within(results).getByText('Webhook: failed, connection refused')).toBeInTheDocument();
  });

  test('a refused test is shown', async () => {
    vi.mocked(api.post).mockRejectedValueOnce(refusal('no channel is configured'));
    renderCard();
    await userEvent.click(await screen.findByRole('button', { name: 'Send test' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('no channel is configured');
  });

  test('an operator reads the settings but cannot change them', async () => {
    renderCard(false);
    expect(await screen.findByLabelText('CPU %')).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Send test' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Clear webhook' })).not.toBeInTheDocument();
  });

  test('a failed read says so', async () => {
    vi.mocked(api.get).mockRejectedValue(new Error('boom'));
    renderCard();
    expect(await screen.findByText('Alert settings could not be loaded.')).toBeInTheDocument();
  });
});
