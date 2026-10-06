import { describe, test, expect, vi, beforeEach } from 'vitest';
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { VaultPage } from './VaultPage';
import { api } from '../api/client';
import { toast } from '../utils/toast';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), delete: vi.fn() } }));
vi.mock('../utils/toast', () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

const PATHS = ['projects/alpha/db', 'projects/beta/db', 'pki/root'];

function stubVault({ sealed = false } = {}) {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === '/vault/status') return Promise.resolve({ data: { sealed } } as never);
    if (url === '/vault/secrets-list') return Promise.resolve({ data: { paths: PATHS } } as never);
    if (url === '/vault/secrets/projects/alpha/db') {
      return Promise.resolve({ data: { username: 'alpha_user', password: 'hunter2' } } as never);
    }
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <VaultPage />
    </QueryClientProvider>,
  );
}

async function confirmDelete(path: string) {
  fireEvent.click(await screen.findByTestId(`vault-delete-${path.replaceAll('/', '-')}`));
  fireEvent.change(screen.getByTestId('confirm-input'), { target: { value: path.split('/').pop() } });
}

describe('VaultPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    stubVault();
  });

  test('hides PKI paths and filters by search', async () => {
    renderPage();
    expect(await screen.findByText('projects/alpha/db')).toBeInTheDocument();
    expect(screen.queryByText('pki/root')).not.toBeInTheDocument();
    expect(screen.getByText('2 secrets')).toBeInTheDocument();

    fireEvent.change(screen.getByTestId('vault-search'), { target: { value: 'BETA' } });
    expect(screen.queryByText('projects/alpha/db')).not.toBeInTheDocument();
    expect(screen.getByText('1 secret')).toBeInTheDocument();

    fireEvent.change(screen.getByTestId('vault-search'), { target: { value: 'nothing-matches' } });
    expect(screen.getByText('No secrets found.')).toBeInTheDocument();
  });

  test('a sealed vault says so instead of listing secrets', async () => {
    stubVault({ sealed: true });
    renderPage();
    expect(await screen.findByText('Vault is Sealed')).toBeInTheDocument();
  });

  test('reveals a secret with the password masked and copies a value', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    renderPage();
    fireEvent.click(await screen.findByTestId('vault-reveal-projects-alpha-db'));

    const values = await screen.findByTestId('vault-secret-values');
    expect(values).toHaveTextContent('alpha_user');
    expect(values).not.toHaveTextContent('hunter2');

    fireEvent.click(screen.getAllByTitle('Copy')[0]);
    expect(writeText).toHaveBeenCalledWith('alpha_user');

    fireEvent.click(screen.getByTestId('vault-reveal-projects-alpha-db'));
    expect(screen.queryByTestId('vault-secret-values')).not.toBeInTheDocument();
  });

  test('a copy the browser refuses says so instead of showing Copied', async () => {
    Object.assign(navigator, { clipboard: { writeText: vi.fn().mockRejectedValue(new Error('denied')) } });
    renderPage();
    fireEvent.click(await screen.findByTestId('vault-reveal-projects-alpha-db'));
    await screen.findByTestId('vault-secret-values');
    fireEvent.click(screen.getAllByTitle('Copy')[0]);
    await waitFor(() => expect(toast.error).toHaveBeenCalledWith('Could not copy to the clipboard'));
    expect(screen.queryByTestId('vault-secret-values')?.querySelector('.lucide-check')).toBeNull();
  });

  test('delete stays disabled until the secret name is typed', async () => {
    renderPage();
    fireEvent.click(await screen.findByTestId('vault-delete-projects-alpha-db'));
    expect(screen.getByTestId('modal-confirm')).toBeDisabled();

    fireEvent.change(screen.getByTestId('confirm-input'), { target: { value: 'wrong' } });
    expect(screen.getByTestId('modal-confirm')).toBeDisabled();
  });

  test('a refused delete shows the server reason, not the status code', async () => {
    vi.mocked(api.delete).mockRejectedValue({
      message: 'Request failed with status code 409',
      response: { status: 409, data: { error: 'secret is in use by project alpha', status: 409 } },
    });
    renderPage();
    await confirmDelete('projects/alpha/db');
    fireEvent.click(screen.getByTestId('modal-confirm'));

    const alert = await screen.findByTestId('vault-delete-error');
    expect(alert).toHaveAttribute('role', 'alert');
    expect(alert).toHaveTextContent('secret is in use by project alpha');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
  });

  test('a double confirm deletes once and closes the dialog', async () => {
    let finish: () => void = () => {};
    vi.mocked(api.delete).mockImplementation(() => new Promise((resolve) => { finish = () => resolve({} as never); }));
    renderPage();
    await confirmDelete('projects/alpha/db');
    const confirm = screen.getByTestId('modal-confirm');
    fireEvent.click(confirm);
    fireEvent.click(confirm);

    await waitFor(() => expect(api.delete).toHaveBeenCalledWith('/vault/secrets/projects/alpha/db'));
    expect(api.delete).toHaveBeenCalledTimes(1);
    await act(async () => finish());
    await waitFor(() => expect(screen.queryByTestId('confirm-modal')).not.toBeInTheDocument());
  });
});
