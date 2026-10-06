import { beforeEach, describe, expect, test, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { PermissionEditor } from './PermissionEditor';
import { api } from '../../api/client';
import type { AnyPermission, Operation } from '../../api/permissions';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), put: vi.fn(), delete: vi.fn() } }));

const REFUSED = {
  message: 'Request failed with status code 409',
  response: { status: 409, data: { error: 'role editor does not exist', status: 409 } },
};

function renderEditor(operation: Operation, existing?: AnyPermission) {
  const onClose = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <QueryClientProvider client={client}>
      <PermissionEditor
        projectId="proj-1"
        table="posts"
        role="editor"
        operation={operation}
        existing={existing}
        columns={['id', 'title', 'owner_id']}
        onClose={onClose}
      />
    </QueryClientProvider>,
  );
  return onClose;
}

function fillValidSelect() {
  fireEvent.click(screen.getAllByRole('button', { name: 'Without any checks' })[0]);
  fireEvent.click(screen.getByLabelText('All columns'));
}

const saveButton = () => screen.getByRole('button', { name: 'Save permission' });

describe('PermissionEditor', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  test('an empty new permission cannot be saved and says what is missing', () => {
    renderEditor('select');
    expect(saveButton()).toBeDisabled();
    expect(screen.getByTestId('filter-error')).toHaveTextContent('Choose a row filter');
    expect(screen.getByTestId('columns-error')).toHaveTextContent('Pick at least one column the role may read.');
  });

  test('a filter that is not JSON is refused before sending', () => {
    renderEditor('select');
    fillValidSelect();
    fireEvent.change(screen.getByLabelText('Row filter'), { target: { value: '{ not json' } });
    expect(screen.getByTestId('filter-error')).toBeInTheDocument();
    expect(saveButton()).toBeDisabled();
  });

  test('a row limit must be a positive whole number within range', () => {
    renderEditor('select');
    fillValidSelect();
    const limit = screen.getByLabelText('Row limit');

    fireEvent.change(limit, { target: { value: '0' } });
    expect(screen.getByTestId('limit-error')).toHaveTextContent('positive whole number');

    fireEvent.change(limit, { target: { value: '99999999999' } });
    expect(screen.getByTestId('limit-error')).toBeInTheDocument();
    expect(saveButton()).toBeDisabled();

    fireEvent.change(limit, { target: { value: '50' } });
    expect(screen.queryByTestId('limit-error')).not.toBeInTheDocument();
    expect(saveButton()).toBeEnabled();
  });

  test('a preset with no column is refused', () => {
    renderEditor('insert');
    fireEvent.click(screen.getByRole('button', { name: 'Without any checks' }));
    fireEvent.click(screen.getByLabelText('All columns'));
    fireEvent.click(screen.getByRole('button', { name: /Add preset/ }));
    expect(screen.getByTestId('presets-error')).toHaveTextContent('Pick a column for every preset.');
    expect(saveButton()).toBeDisabled();
  });

  test('saves a valid permission and closes', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: {} } as never);
    const onClose = renderEditor('select');
    fillValidSelect();
    fireEvent.click(saveButton());

    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(api.put).toHaveBeenCalledWith('/provision/proj-1/permissions/tables/posts/roles/editor/select', {
      filter: {},
      columns: '*',
      allowAggregations: false,
    });
  });

  test("a refused save shows the server's reason, not the status code", async () => {
    vi.mocked(api.put).mockRejectedValue(REFUSED);
    const onClose = renderEditor('select');
    fillValidSelect();
    fireEvent.click(saveButton());

    const alert = await screen.findByTestId('permission-save-error');
    expect(alert).toHaveTextContent('role editor does not exist');
    expect(alert).toHaveAttribute('role', 'alert');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
    expect(onClose).not.toHaveBeenCalled();
  });

  test('two quick clicks on save send one request', async () => {
    vi.mocked(api.put).mockReturnValue(new Promise(() => {}) as never);
    renderEditor('select');
    fillValidSelect();
    const save = saveButton();
    fireEvent.click(save);
    fireEvent.click(save);

    await waitFor(() => expect(api.put).toHaveBeenCalled());
    expect(api.put).toHaveBeenCalledTimes(1);
  });

  test("a refused remove shows the server's reason", async () => {
    vi.mocked(api.delete).mockRejectedValue(REFUSED);
    renderEditor('delete', { filter: {} });
    fireEvent.click(screen.getByRole('button', { name: 'Remove permission' }));
    fireEvent.click(screen.getByTestId('modal-confirm'));

    expect(await screen.findByTestId('permission-save-error')).toHaveTextContent('role editor does not exist');
    expect(screen.queryByText(/status code/)).not.toBeInTheDocument();
    expect(api.delete).toHaveBeenCalledWith('/provision/proj-1/permissions/tables/posts/roles/editor/delete');
  });

  test('a removed permission closes the editor', async () => {
    vi.mocked(api.delete).mockResolvedValue({} as never);
    const onClose = renderEditor('update', { filter: {}, check: {}, columns: '*' });
    fireEvent.click(screen.getByRole('button', { name: 'Remove permission' }));
    fireEvent.click(screen.getByTestId('modal-confirm'));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
  });
});
