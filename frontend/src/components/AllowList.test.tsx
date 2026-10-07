import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { AllowList } from './AllowList';

function renderList(props: Partial<Parameters<typeof AllowList>[0]> = {}) {
  const onAdd = vi.fn();
  const onRemove = vi.fn();
  render(
    <AllowList
      name="things"
      entries={['a.example.com:443']}
      emptyText="none"
      inputLabel="Thing"
      placeholder="x"
      addLabel="Add thing"
      busy={false}
      error={null}
      onAdd={onAdd}
      onRemove={onRemove}
      {...props}
    />,
  );
  return { onAdd, onRemove };
}

describe('AllowList', () => {
  test('Enter adds the trimmed entry and the field clears once the add is done', async () => {
    const { onAdd } = renderList();
    await userEvent.type(screen.getByLabelText('Thing'), '  b.example.com {Enter}');
    expect(onAdd).toHaveBeenCalledWith('b.example.com', expect.any(Function));
    expect(screen.getByLabelText('Thing')).toHaveValue('  b.example.com ');
    onAdd.mock.calls[0][1]();
    expect(await screen.findByLabelText('Thing')).toHaveValue('');
  });

  test('while a change is in flight nothing else is sent', async () => {
    const { onAdd, onRemove } = renderList({ busy: true });
    await userEvent.type(screen.getByLabelText('Thing'), 'b.example.com{Enter}');
    expect(onAdd).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Remove a.example.com:443' })).toBeDisabled();
    expect(onRemove).not.toHaveBeenCalled();
  });

  test('a refusal without a reason falls back to a plain sentence', () => {
    renderList({ error: { response: { status: 400, data: {} } } });
    expect(screen.getByRole('alert')).toHaveTextContent('The change was not made');
  });
});
