import { describe, expect, test, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { ColumnChecklist } from './ColumnChecklist';
import type { ColumnList } from '../../api/permissions';

const COLUMNS = ['id', 'title', 'owner_id'];

function renderChecklist(value: ColumnList, error?: string) {
  const onChange = vi.fn();
  render(<ColumnChecklist legend="Columns the role can read" columns={COLUMNS} value={value} error={error} onChange={onChange} />);
  return onChange;
}

describe('ColumnChecklist', () => {
  test('"All columns" ticks every column and locks them', () => {
    renderChecklist('*');
    expect(screen.getByLabelText('All columns')).toBeChecked();
    for (const column of COLUMNS) {
      expect(screen.getByLabelText(column)).toBeChecked();
      expect(screen.getByLabelText(column)).toBeDisabled();
    }
  });

  test('unticking "All columns" clears the list', () => {
    const onChange = renderChecklist('*');
    fireEvent.click(screen.getByLabelText('All columns'));
    expect(onChange).toHaveBeenCalledWith([]);
  });

  test('ticking "All columns" from a list gives the wildcard', () => {
    const onChange = renderChecklist(['id']);
    fireEvent.click(screen.getByLabelText('All columns'));
    expect(onChange).toHaveBeenCalledWith('*');
  });

  test('ticked columns keep the table order', () => {
    const onChange = renderChecklist(['owner_id']);
    fireEvent.click(screen.getByLabelText('id'));
    expect(onChange).toHaveBeenCalledWith(['id', 'owner_id']);
  });

  test('unticking a column removes it', () => {
    const onChange = renderChecklist(['id', 'title']);
    fireEvent.click(screen.getByLabelText('title'));
    expect(onChange).toHaveBeenCalledWith(['id']);
  });

  test('an empty choice shows its error', () => {
    renderChecklist([], 'Pick at least one column the role may read.');
    expect(screen.getByTestId('columns-error')).toHaveTextContent('Pick at least one column the role may read.');
  });
});
