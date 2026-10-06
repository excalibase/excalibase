import { describe, test, expect, beforeEach, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AxiosError } from 'axios';
import { CreateTablePanel } from './CreateTablePanel';
import { api } from '../../api/client';

vi.mock('../../api/client', () => ({
  api: { post: vi.fn(), put: vi.fn() },
}));

function renderPanel(onClose = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <CreateTablePanel open onClose={onClose} projectId="p1" />
    </QueryClientProvider>,
  );
  return onClose;
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.post).mockResolvedValue({ data: {} } as never);
});

describe('CreateTablePanel columns', () => {
  test('edits, adds and removes columns before creating', async () => {
    const onClose = renderPanel();
    const user = userEvent.setup();
    await user.type(screen.getByTestId('table-name-input'), 'notes');
    await user.click(screen.getByText('+ Add column'));
    const names = screen.getAllByPlaceholderText('e.g. email');
    const types = screen.getAllByPlaceholderText('e.g. text');
    await user.type(names[1], 'body');
    await user.clear(types[1]);
    await user.type(types[1], 'varchar');
    await user.click(screen.getByText('+ Add column'));
    await user.click(screen.getAllByRole('button', { name: 'Remove column' })[1]);
    await user.click(screen.getByTestId('create-table-submit'));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith('/schema/p1/tables', {
        schema: 'public',
        name: 'notes',
        columns: [
          { name: 'id', type: 'serial', primaryKey: true, nullable: false, unique: false, default: undefined },
          { name: 'body', type: 'varchar', primaryKey: false, nullable: true, unique: false, default: undefined },
        ],
      }),
    );
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    // Without canGrantRead no permission is offered or written.
    expect(screen.queryByLabelText('Anyone can read (anon)')).not.toBeInTheDocument();
    expect(api.put).not.toHaveBeenCalled();
  });

  test('a failed create keeps the panel and writes no permission', async () => {
    vi.mocked(api.post).mockRejectedValue(new Error('exists'));
    const onClose = renderPanel();
    const user = userEvent.setup();
    await user.type(screen.getByTestId('table-name-input'), 'notes');
    await user.click(screen.getByTestId('create-table-submit'));
    await waitFor(() => expect(api.post).toHaveBeenCalled());
    expect(onClose).not.toHaveBeenCalled();
    expect(api.put).not.toHaveBeenCalled();
  });

  test("the server's reason is shown in the panel, not a status code", async () => {
    vi.mocked(api.post).mockRejectedValue(
      new AxiosError('Request failed with status code 409', 'ERR_BAD_REQUEST', undefined, undefined, {
        status: 409, data: { error: 'relation "notes" already exists', status: 409 },
      } as never),
    );
    renderPanel();
    const user = userEvent.setup();
    await user.type(screen.getByTestId('table-name-input'), 'notes');
    await user.click(screen.getByTestId('create-table-submit'));
    expect(await screen.findByTestId('create-table-error')).toHaveTextContent('relation "notes" already exists');
    expect(screen.queryByText(/Request failed with status code/)).not.toBeInTheDocument();
  });

  test('a double click creates the table once', async () => {
    let finish: (value: unknown) => void = () => {};
    vi.mocked(api.post).mockImplementation(() => new Promise((resolve) => { finish = resolve; }) as never);
    renderPanel();
    const user = userEvent.setup();
    await user.type(screen.getByTestId('table-name-input'), 'notes');
    const submit = screen.getByTestId('create-table-submit');
    // Both clicks land before React re-renders, as a fast double click does in a browser.
    fireEvent.click(submit);
    fireEvent.click(submit);
    finish({ data: {} });
    await waitFor(() => expect(api.post).toHaveBeenCalledTimes(1));
  });
});
