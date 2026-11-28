import { describe, test, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { api } from '../../../api/client';
import { FunctionLogsTab } from './FunctionLogsTab';

vi.mock('../../../api/client', () => ({ api: { get: vi.fn() } }));

const fn = (id: string) => ({ id, projectId: 'p1', name: id, files: [], active: true, version: 1, createdAt: '', updatedAt: '' });
const FUNCTIONS = [fn('hello'), fn('bye')];

function renderTab(functions = FUNCTIONS) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <FunctionLogsTab projectId="p1" functions={functions} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.get).mockResolvedValue({ data: { logs: [] } } as never);
});

describe('FunctionLogsTab', () => {
  test('reads the first function and prints its lines with level and message', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { logs: [{ level: 'warn', msg: 'slow call', ts: 1760000000000 }] } } as never);
    renderTab();
    expect(await screen.findByText('slow call')).toBeInTheDocument();
    expect(screen.getByText('warn')).toBeInTheDocument();
    expect(api.get).toHaveBeenCalledWith('/projects/p1/functions/hello/logs');
  });

  test('choosing another function reads that one', async () => {
    renderTab();
    await userEvent.selectOptions(await screen.findByRole('combobox', { name: 'Function' }), 'bye');
    expect(await screen.findByText(/No logs yet/)).toBeInTheDocument();
    expect(api.get).toHaveBeenCalledWith('/projects/p1/functions/bye/logs');
  });

  test('a refused read is shown, not turned into "No logs yet"', async () => {
    vi.mocked(api.get).mockRejectedValue(Object.assign(new Error('x'), { response: { status: 503, data: { error: 'vault is sealed' } } }));
    renderTab();
    expect(await screen.findByRole('alert')).toHaveTextContent(/vault is sealed/);
    expect(screen.queryByText(/No logs yet/)).not.toBeInTheDocument();
  });

  test('with no functions there is nothing to read', () => {
    renderTab([]);
    expect(screen.getByText('No functions in this project.')).toBeInTheDocument();
    expect(api.get).not.toHaveBeenCalled();
  });
});
