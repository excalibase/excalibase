import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { S3AccessPanel } from './S3AccessPanel';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({
  api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() },
}));

const listed = {
  keys: [
    {
      id: 's3k_1', projectId: 'proj-1', name: 'nas backup', accessKeyId: 'EXS3AAAAAAAAAAAAAAAA', buckets: [],
      read: true, write: true, delete: false, createdBy: 'u', createdAt: '2026-10-18T01:00:00Z',
    },
  ],
  endpoint: 'https://s3.example.test',
  region: 'us-east-1',
};

function renderPanel() {
  vi.mocked(api.get).mockResolvedValue({ data: listed } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <S3AccessPanel projectId="proj-1" buckets={[{ name: 'photos' }, { name: 'backups' }]} />
    </QueryClientProvider>,
  );
}

describe('S3 access keys', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.spyOn(window, 'confirm').mockReturnValue(true);
  });

  test('lists keys by access key id, scope and permissions, with the endpoint', async () => {
    renderPanel();
    const row = await screen.findByTestId('s3-key-s3k_1');
    expect(row).toHaveTextContent('nas backup');
    expect(row).toHaveTextContent('EXS3AAAAAAAAAAAAAAAA');
    expect(row).toHaveTextContent('All buckets');
    expect(row).toHaveTextContent('read, write');
    expect(screen.getByTestId('s3-endpoint')).toHaveTextContent('https://s3.example.test');
    expect(api.get).toHaveBeenCalledWith('/projects/proj-1/storage/s3-keys');
  });

  test('creates a scoped key and shows the secret once with client settings', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockResolvedValue({
      data: {
        ...listed.keys[0], id: 's3k_2', name: 'ci', buckets: ['photos'], read: true, write: false, delete: false,
        secretAccessKey: 'SECRET-SHOWN-ONCE', endpoint: 'https://s3.example.test', region: 'us-east-1',
      },
    } as never);
    renderPanel();
    await screen.findByTestId('s3-key-s3k_1');
    await u.type(screen.getByLabelText('Key name'), 'ci');
    await u.click(screen.getByLabelText('Only these buckets'));
    await u.click(screen.getByLabelText('photos'));
    await u.type(screen.getByLabelText('Expires after (days)'), '30');
    await u.click(screen.getByRole('button', { name: /create key/i }));

    await waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(api.post).toHaveBeenCalledWith('/projects/proj-1/storage/s3-keys', {
      name: 'ci', buckets: ['photos'], permissions: { read: true, write: false, delete: false }, expiresInDays: 30,
    });
    const notice = await screen.findByTestId('new-s3-key');
    expect(notice).toHaveTextContent('SECRET-SHOWN-ONCE');
    expect(notice).toHaveTextContent('aws configure set default.s3.addressing_style path');
    expect(notice).toHaveTextContent('--endpoint-url https://s3.example.test');
    expect(notice).toHaveTextContent('provider = Other');
    await u.click(within(notice).getByRole('button', { name: /i have saved it/i }));
    expect(screen.queryByText('SECRET-SHOWN-ONCE')).toBeNull();
  });

  test('a key with no permission is not sent', async () => {
    const u = userEvent.setup();
    renderPanel();
    await screen.findByTestId('s3-key-s3k_1');
    await u.type(screen.getByLabelText('Key name'), 'nothing');
    await u.click(screen.getByLabelText('Read'));
    await u.click(screen.getByRole('button', { name: /create key/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/at least one/i);
    expect(api.post).not.toHaveBeenCalled();
  });

  test('revokes a key after confirmation', async () => {
    const u = userEvent.setup();
    vi.mocked(api.delete).mockResolvedValue({} as never);
    renderPanel();
    const row = await screen.findByTestId('s3-key-s3k_1');
    await u.click(within(row).getByRole('button', { name: /revoke/i }));
    expect(api.delete).toHaveBeenCalledWith('/projects/proj-1/storage/s3-keys/s3k_1');
  });

  test('shows the server refusal', async () => {
    const u = userEvent.setup();
    vi.mocked(api.post).mockRejectedValue({ response: { data: { error: 'the project already holds 20 S3 access keys' } } });
    renderPanel();
    await screen.findByTestId('s3-key-s3k_1');
    await u.type(screen.getByLabelText('Key name'), 'one more');
    await u.click(screen.getByRole('button', { name: /create key/i }));
    expect(await screen.findByRole('alert')).toHaveTextContent('already holds 20');
  });
});
