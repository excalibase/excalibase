import { describe, test, expect, beforeEach, vi } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import { EditorView } from '@codemirror/view';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { SqlEditorPage } from './SqlEditorPage';
import { api } from '../api/client';
import { typeIntoEditor } from '../test/codemirror';

vi.mock('../api/client', () => ({ api: { get: vi.fn(), post: vi.fn() } }));

function renderPage() {
  vi.mocked(api.get).mockResolvedValue({
    data: [{ name: 'users', schema: 'public', type: 'table', comment: null }],
  } as never);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={['/project/p1/sql']}>
        <Routes>
          <Route path="/project/:projectId/sql" element={<SqlEditorPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

async function run(container: HTMLElement, text: string) {
  typeIntoEditor(container, text);
  await userEvent.click(screen.getByTestId('run-query-btn'));
}

describe('SqlEditorPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
  });

  test("a refused query shows the server's reason where a SQL error would be", async () => {
    vi.mocked(api.post).mockRejectedValueOnce({
      message: 'Request failed with status code 409',
      response: { status: 409, data: { error: 'the database is paused' } },
    });
    const { container } = renderPage();
    await run(container, 'SELECT 1');

    expect(await screen.findByTestId('query-error')).toHaveTextContent('the database is paused');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
    expect(api.post).toHaveBeenCalledWith('/schema/p1/query', { query: 'SELECT 1' });
  });

  test('a query that never reaches the server says so', async () => {
    vi.mocked(api.post).mockRejectedValueOnce(Object.assign(new Error('Network Error'), { isAxiosError: true }));
    const { container } = renderPage();
    await run(container, 'SELECT 1');

    expect(await screen.findByTestId('query-error')).toHaveTextContent('The query could not run: the server could not be reached.');
  });

  test('shows the rows a query returns, NULLs and objects included', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({
      data: { columns: [{ name: 'id', dataType: 'INT4' }, { name: 'meta', dataType: 'JSONB' }], rows: [[1, null], [2, { a: 1 }]] },
    } as never);
    const { container } = renderPage();
    await run(container, 'SELECT * FROM users');

    expect(await screen.findByText('2 rows')).toBeInTheDocument();
    expect(screen.getByText('NULL')).toBeInTheDocument();
    expect(screen.getByText('{"a":1}')).toBeInTheDocument();
  });

  test('a row JSON cannot serialise still renders', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({
      data: { columns: [{ name: 'n', dataType: 'INT8' }], rows: [[10n]] },
    } as never);
    const { container } = renderPage();
    await run(container, 'SELECT 10');

    expect(await screen.findByText('1 row')).toBeInTheDocument();
    expect(screen.getByRole('cell', { name: '10' })).toBeInTheDocument();
  });

  test('runs only the selected text, and says when the server cut the rows short', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: { columns: [{ name: 'x', dataType: 'JSONB' }], truncated: true } } as never);
    const { container } = renderPage();
    // The table list rebuilds the editor once it arrives; select after that.
    await act(async () => {});
    typeIntoEditor(container, 'SELECT 1; SELECT 2');
    const view = EditorView.findFromDOM(container.querySelector('.cm-editor') as HTMLElement)!;
    act(() => view.dispatch({ selection: { anchor: 10, head: 18 } }));
    await userEvent.click(screen.getByTestId('run-query-btn'));

    expect(api.post).toHaveBeenCalledWith('/schema/p1/query', { query: 'SELECT 2' });
    expect(await screen.findByTestId('query-truncated')).toBeInTheDocument();
  });

  test('a cell that cannot be serialised renders empty instead of failing', async () => {
    const circular: Record<string, unknown> = {};
    circular.self = circular;
    vi.mocked(api.post).mockResolvedValueOnce({ data: { columns: [{ name: 'c', dataType: 'JSONB' }], rows: [[circular]] } } as never);
    const { container } = renderPage();
    await run(container, 'SELECT c');

    expect(await screen.findByText('1 row')).toBeInTheDocument();
    expect(screen.getByRole('cell')).toHaveTextContent('');
  });

  test('a statement without rows reports how many it touched', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: { command: 'UPDATE', affectedRows: 3 } } as never);
    const { container } = renderPage();
    await run(container, 'UPDATE users SET x = 1');

    expect(await screen.findByTestId('query-success')).toHaveTextContent('UPDATE: 3 rows affected');
  });

  test('an empty editor runs nothing', async () => {
    const { container } = renderPage();
    await run(container, '   ');
    expect(api.post).not.toHaveBeenCalled();
  });

  test('history keeps a run query, loads it back and can be cleared', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { command: 'SELECT', affectedRows: undefined } } as never);
    const { container } = renderPage();
    await run(container, 'SELECT 42');
    await screen.findByTestId('query-success');
    expect(screen.getByTestId('query-success')).toHaveTextContent('SELECT: 0 rows affected');

    await userEvent.click(screen.getByTestId('history-btn'));
    await userEvent.click(screen.getByRole('button', { name: 'SELECT 42' }));
    expect(screen.queryByTestId('query-history')).not.toBeInTheDocument();

    await userEvent.click(screen.getByTestId('history-btn'));
    await userEvent.click(screen.getByRole('button', { name: /Clear history/ }));
    expect(screen.getByTestId('history-btn')).toHaveTextContent('History (0)');
  });

  test('tabs can be added, switched and closed', async () => {
    renderPage();
    await userEvent.click(screen.getByTestId('add-tab-btn'));
    expect(screen.getByRole('button', { name: /^Query 2/ })).toBeInTheDocument();

    await userEvent.click(screen.getByRole('button', { name: /^Query 1/ }));
    await userEvent.click(screen.getByRole('button', { name: 'Close Query 1' }));
    await waitFor(() => expect(screen.queryByRole('button', { name: /^Query 1/ })).not.toBeInTheDocument());
    expect(screen.queryByRole('button', { name: /^Close/ })).not.toBeInTheDocument();
  });

  test('unreadable saved tabs and history fall back to a fresh editor', () => {
    localStorage.setItem('sql_editor_tabs', '{');
    localStorage.setItem('sql_editor_history', '{');
    renderPage();
    expect(screen.getByRole('button', { name: /^Query 1/ })).toBeInTheDocument();
    expect(screen.getByTestId('history-btn')).toHaveTextContent('History (0)');
  });
});
