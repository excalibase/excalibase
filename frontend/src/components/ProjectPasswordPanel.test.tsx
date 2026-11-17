import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ProjectPasswordPanel } from './ProjectPasswordPanel';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const CREDENTIALS = { projectId: 'p-1', databaseName: 'app', username: 'app', sslMode: 'require', port: 5432 };

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ProjectPasswordPanel projectId="p-1" password="not-a-real-password" onDone={() => undefined} />
    </QueryClientProvider>,
  );
}

function answerGets(credentials: () => Promise<unknown>) {
  vi.mocked(api.get).mockImplementation((path: string) => {
    if (path.endsWith('/credentials')) return credentials() as never;
    return Promise.reject(new Error('no endpoint')) as never;
  });
}

beforeEach(() => vi.clearAllMocks());

describe('ProjectPasswordPanel', () => {
  test('shows the login name and database once the credentials answer', async () => {
    answerGets(() => Promise.resolve({ data: CREDENTIALS }));
    renderPanel();
    await waitFor(() => expect(screen.getByTestId('otp-user')).toHaveTextContent('app'));
    expect(screen.getByTestId('otp-database')).toHaveTextContent('app');
  });

  test('a database still being built says so, then fills in the login name when it is ready', async () => {
    let calls = 0;
    answerGets(() => (++calls < 2 ? Promise.reject(new Error('not ready')) : Promise.resolve({ data: CREDENTIALS })));
    renderPanel();
    await waitFor(() => expect(screen.getByTestId('otp-user')).toHaveTextContent(/ready/i));
    expect(screen.getByTestId('otp-user')).not.toHaveTextContent('—');
    await waitFor(() => expect(screen.getByTestId('otp-user')).toHaveTextContent(/^app$/), { timeout: 8000 });
    expect(screen.getByTestId('otp-database')).toHaveTextContent(/^app$/);
  }, 15000);

  test('the CSV download is disabled until the login is known, then enabled', async () => {
    let calls = 0;
    answerGets(() => (++calls < 2 ? Promise.reject(new Error('not ready')) : Promise.resolve({ data: CREDENTIALS })));
    renderPanel();
    const waiting = await screen.findByRole('button', { name: /download csv/i });
    expect(waiting).toBeDisabled();
    expect(screen.getByTestId('otp-csv-reason')).toHaveTextContent('Available when the database is ready');
    expect(screen.getByTestId('otp-password')).toHaveTextContent('not-a-real-password');
    const link = await screen.findByRole('link', { name: /download csv/i }, { timeout: 8000 });
    const csv = decodeURIComponent(link.getAttribute('href')!);
    expect(csv).toContain('"app","app","not-a-real-password"');
    expect(screen.queryByTestId('otp-csv-reason')).not.toBeInTheDocument();
  }, 15000);

  test('when the login cannot be loaded it says where to find it and keeps the CSV disabled', async () => {
    answerGets(() => Promise.reject({ response: { status: 403 } }));
    renderPanel();
    await waitFor(() => expect(screen.getByTestId('otp-user')).toHaveTextContent(/Couldn.t load the login name/));
    expect(screen.getByTestId('otp-user')).toHaveTextContent(/Connection Credentials/);
    expect(screen.getByRole('button', { name: /download csv/i })).toBeDisabled();
    expect(screen.getByTestId('otp-csv-reason')).toHaveTextContent(/Connection Credentials/);
    expect(screen.getByTestId('otp-password')).toHaveTextContent('not-a-real-password');
  });
});
