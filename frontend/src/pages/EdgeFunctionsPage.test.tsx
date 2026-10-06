import { describe, test, expect, vi, beforeEach } from 'vitest';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { EdgeFunctionsPage } from './EdgeFunctionsPage';
import { api } from '../api/client';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

const BASE = '/projects/proj-1/functions';
const HELLO = {
  id: 'hello', projectId: 'proj-1', name: 'Hello', active: true, version: 2,
  files: [{ path: 'index.ts', content: 'export default () => new Response("hi");' }],
  createdAt: '2026-01-01T00:00:00Z', updatedAt: '2026-01-01T00:00:00Z',
};
const LOGS = [
  { level: 'error', msg: 'boom', ts: 1 },
  { level: 'warn', msg: 'careful', ts: 2 },
  { level: 'info', msg: 'hello', ts: 3 },
  { level: 'debug', msg: 'detail', ts: 4 },
];

const refusal = (reason: string) => ({
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: reason, status: 409 } },
});

const pending = () => new Promise<never>(() => {});

function stubReads() {
  vi.mocked(api.get).mockImplementation((url: string) => {
    if (url === BASE) return Promise.resolve({ data: [HELLO] } as never);
    if (url === `${BASE}/runtime/status`) return Promise.resolve({ data: { status: 'running', healthy: true } } as never);
    if (url === `${BASE}/secrets`) return Promise.resolve({ data: [{ key: 'STRIPE_KEY' }] } as never);
    if (url === `${BASE}/hello/logs`) return Promise.resolve({ data: { logs: LOGS } } as never);
    return Promise.reject(new Error(`unexpected GET ${url}`));
  });
}

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/proj-1/functions']}>
        <Routes>
          <Route path="/project/:projectId/functions" element={<EdgeFunctionsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function openDeployForm() {
  fireEvent.click(await screen.findByTestId('create-fn-btn'));
}

async function openSecrets() {
  fireEvent.click(await screen.findByTestId('secrets-btn'));
}

function fillSecret(key: string, value: string) {
  fireEvent.change(screen.getByLabelText('Key'), { target: { value: key } });
  fireEvent.change(screen.getByLabelText('Value'), { target: { value } });
}

describe('EdgeFunctionsPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    stubReads();
  });

  describe('deploy form', () => {
    test('Deploy stays disabled until both id and name are filled', async () => {
      renderPage();
      await openDeployForm();
      const submit = screen.getByTestId('submit-fn-btn');
      expect(submit).toBeDisabled();

      fireEvent.change(screen.getByTestId('fn-id-input'), { target: { value: '   ' } });
      fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Hello' } });
      expect(submit).toBeDisabled();
    });

    test('a refused deploy shows the server reason, not the status code', async () => {
      vi.mocked(api.post).mockRejectedValue(refusal('function id "hello" already exists'));
      renderPage();
      await openDeployForm();
      fireEvent.change(screen.getByTestId('fn-id-input'), { target: { value: 'hello' } });
      fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Hello' } });
      fireEvent.click(screen.getByTestId('submit-fn-btn'));

      expect(await screen.findByText('function id "hello" already exists')).toBeInTheDocument();
      expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
    });

    test('a double click on Deploy sends one request', async () => {
      vi.mocked(api.post).mockImplementation(pending);
      renderPage();
      await openDeployForm();
      fireEvent.change(screen.getByTestId('fn-id-input'), { target: { value: 'hello' } });
      fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Hello' } });
      const submit = screen.getByTestId('submit-fn-btn');
      fireEvent.click(submit);
      fireEvent.click(submit);

      await waitFor(() => expect(api.post).toHaveBeenCalled());
      expect(api.post).toHaveBeenCalledTimes(1);
    });

    test('deploys every file and closes the panel on success', async () => {
      vi.mocked(api.post).mockResolvedValue({ data: HELLO } as never);
      renderPage();
      await openDeployForm();
      fireEvent.change(screen.getByTestId('fn-id-input'), { target: { value: 'hello' } });
      fireEvent.change(screen.getByLabelText('Display name'), { target: { value: 'Hello' } });
      fireEvent.change(screen.getByTestId('fn-code-input'), { target: { value: 'main();' } });
      fireEvent.click(screen.getByTestId('add-file-btn'));
      fireEvent.change(screen.getByDisplayValue('helper-1.ts'), { target: { value: 'util.ts' } });
      fireEvent.change(screen.getByTestId('fn-code-input'), { target: { value: 'export const one = 1;' } });
      fireEvent.click(screen.getByTestId('add-file-btn'));
      const extraTab = screen.getByText('helper-2.ts').parentElement as HTMLElement;
      fireEvent.click(within(extraTab).getAllByRole('button')[1]);
      expect(screen.queryByText('helper-2.ts')).not.toBeInTheDocument();
      fireEvent.click(screen.getByTestId('submit-fn-btn'));

      await waitFor(() =>
        expect(api.post).toHaveBeenCalledWith(BASE, {
          id: 'hello',
          name: 'Hello',
          files: [
            { path: 'index.ts', content: 'main();' },
            { path: 'util.ts', content: 'export const one = 1;' },
          ],
        }),
      );
      await waitFor(() => expect(screen.queryByTestId('submit-fn-btn')).not.toBeInTheDocument());
    });
  });

  describe('secrets', () => {
    test('Save secret stays disabled until key and value are filled, and keys are upper-cased', async () => {
      renderPage();
      await openSecrets();
      expect(screen.getByTestId('save-secret-btn')).toBeDisabled();
      fillSecret('stripe_key', '');
      expect(screen.getByLabelText('Key')).toHaveValue('STRIPE_KEY');
      expect(screen.getByTestId('save-secret-btn')).toBeDisabled();
    });

    test('a refused secret shows the server reason, not the status code', async () => {
      vi.mocked(api.post).mockRejectedValue(refusal('key must be at most 64 characters'));
      renderPage();
      await openSecrets();
      fillSecret('A'.repeat(65), 'value');
      fireEvent.click(screen.getByTestId('save-secret-btn'));

      expect(await screen.findByText('key must be at most 64 characters')).toBeInTheDocument();
      expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
    });

    test('a double click on Save secret sends one request', async () => {
      vi.mocked(api.post).mockImplementation(pending);
      renderPage();
      await openSecrets();
      fillSecret('STRIPE_KEY', 'sk_live');
      const save = screen.getByTestId('save-secret-btn');
      fireEvent.click(save);
      fireEvent.click(save);

      await waitFor(() => expect(api.post).toHaveBeenCalled());
      expect(api.post).toHaveBeenCalledTimes(1);
    });

    test('a saved secret clears the form', async () => {
      vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
      renderPage();
      await openSecrets();
      fillSecret('STRIPE_KEY', 'sk_live');
      fireEvent.click(screen.getByTestId('save-secret-btn'));

      await waitFor(() => expect(screen.getByLabelText('Key')).toHaveValue(''));
      expect(api.post).toHaveBeenCalledWith(`${BASE}/secrets`, { key: 'STRIPE_KEY', value: 'sk_live' });
    });

    test('a refused secret delete shows the server reason', async () => {
      vi.mocked(api.delete).mockRejectedValue(refusal('STRIPE_KEY is used by function hello'));
      renderPage();
      await openSecrets();
      const row = (await screen.findByText('STRIPE_KEY')).parentElement as HTMLElement;
      fireEvent.click(within(row).getByRole('button'));

      const alert = await screen.findByTestId('edge-secret-delete-error');
      expect(alert).toHaveAttribute('role', 'alert');
      expect(alert).toHaveTextContent('STRIPE_KEY is used by function hello');
      expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
    });

    test('a double click on a secret delete sends one request', async () => {
      vi.mocked(api.delete).mockImplementation(pending);
      renderPage();
      await openSecrets();
      const row = (await screen.findByText('STRIPE_KEY')).parentElement as HTMLElement;
      const remove = within(row).getByRole('button');
      fireEvent.click(remove);
      fireEvent.click(remove);

      await waitFor(() => expect(api.delete).toHaveBeenCalled());
      expect(api.delete).toHaveBeenCalledTimes(1);
    });
  });

  describe('bulk .env import', () => {
    test('lines in the wrong format are reported and nothing is sent', async () => {
      renderPage();
      await openSecrets();
      fireEvent.change(screen.getByTestId('env-paste-textarea'), {
        target: { value: '# comment\n\nlowercase=1\nNO_EQUALS' },
      });
      fireEvent.click(screen.getByTestId('env-paste-import-btn'));

      const status = screen.getByTestId('env-paste-status');
      expect(status).toHaveTextContent('line 3: invalid key "lowercase" (must be UPPER_SNAKE)');
      expect(status).toHaveTextContent("line 4: missing '='");
      expect(api.post).not.toHaveBeenCalled();
    });

    test('imports valid lines, strips quotes and export, and says what was skipped', async () => {
      vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
      renderPage();
      await openSecrets();
      fireEvent.change(screen.getByTestId('env-paste-textarea'), {
        target: { value: 'export API_URL="https://x"\nTOKEN=\'abc\'\nbad key=1' },
      });
      expect(screen.getByTestId('env-paste-import-btn')).toHaveTextContent('Import from .env (2)');
      fireEvent.click(screen.getByTestId('env-paste-import-btn'));

      expect(await screen.findByText('saved 2/2, 1 skipped')).toBeInTheDocument();
      expect(api.post).toHaveBeenCalledWith(`${BASE}/secrets`, { key: 'API_URL', value: 'https://x' });
      expect(api.post).toHaveBeenCalledWith(`${BASE}/secrets`, { key: 'TOKEN', value: 'abc' });
    });

    test('a refused line shows the server reason, not the status code', async () => {
      vi.mocked(api.post).mockRejectedValue(refusal('value is larger than 4 KB'));
      renderPage();
      await openSecrets();
      fireEvent.change(screen.getByTestId('env-paste-textarea'), { target: { value: 'BIG=xxx' } });
      fireEvent.click(screen.getByTestId('env-paste-import-btn'));

      const status = await screen.findByText(/saved 0\/1/);
      expect(status).toHaveTextContent('BIG: value is larger than 4 KB');
      expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
    });

    test('a double click on Import sends each line once', async () => {
      vi.mocked(api.post).mockImplementation(pending);
      renderPage();
      await openSecrets();
      fireEvent.change(screen.getByTestId('env-paste-textarea'), { target: { value: 'ONE=1' } });
      const importButton = screen.getByTestId('env-paste-import-btn');
      fireEvent.click(importButton);
      fireEvent.click(importButton);

      await waitFor(() => expect(api.post).toHaveBeenCalled());
      expect(api.post).toHaveBeenCalledTimes(1);
    });
  });

  describe('a selected function', () => {
    async function selectHello() {
      fireEvent.click(await screen.findByTestId('fn-item-hello'));
    }

    test('invoke shows the answer, or the reason it failed', async () => {
      vi.mocked(api.post).mockResolvedValueOnce({ data: '{"message":"Hello world"}' } as never);
      renderPage();
      await selectHello();
      expect(screen.getByTestId('fn-code')).toHaveTextContent('new Response');
      fireEvent.click(screen.getByTestId('invoke-btn'));
      expect(await screen.findByText('{"message":"Hello world"}')).toBeInTheDocument();

      vi.mocked(api.post).mockRejectedValueOnce(refusal('function hello is not deployed'));
      fireEvent.click(screen.getByTestId('invoke-btn'));
      expect(await screen.findByText('Error: function hello is not deployed')).toBeInTheDocument();
    });

    test('logs panel lists each line', async () => {
      renderPage();
      await selectHello();
      fireEvent.click(screen.getByTestId('logs-btn'));
      const panel = await screen.findByTestId('logs-panel');
      await waitFor(() => expect(panel).toHaveTextContent('boom'));
      expect(panel).toHaveTextContent('detail');
    });

    test('a refused delete shows the server reason, not the status code', async () => {
      vi.mocked(api.delete).mockRejectedValue(refusal('function hello is referenced by a cron job'));
      renderPage();
      await selectHello();
      fireEvent.click(screen.getByTestId('delete-fn-btn'));
      fireEvent.change(screen.getByTestId('confirm-input'), { target: { value: 'hello' } });
      fireEvent.click(screen.getByTestId('modal-confirm'));

      const alert = await screen.findByTestId('delete-fn-error');
      expect(alert).toHaveAttribute('role', 'alert');
      expect(alert).toHaveTextContent('function hello is referenced by a cron job');
      expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
    });

    test('a double confirm deletes once and clears the selection', async () => {
      let finish: () => void = () => {};
      vi.mocked(api.delete).mockImplementation(() => new Promise((resolve) => { finish = () => resolve({} as never); }));
      renderPage();
      await selectHello();
      fireEvent.click(screen.getByTestId('delete-fn-btn'));
      fireEvent.change(screen.getByTestId('confirm-input'), { target: { value: 'hello' } });
      const confirm = screen.getByTestId('modal-confirm');
      fireEvent.click(confirm);
      fireEvent.click(confirm);

      await waitFor(() => expect(api.delete).toHaveBeenCalledWith(`${BASE}/hello`));
      expect(api.delete).toHaveBeenCalledTimes(1);
      await act(async () => finish());
      expect(await screen.findByText('Select a function to view its source, test, and logs')).toBeInTheDocument();
    });
  });
});
