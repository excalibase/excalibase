import { describe, expect, test, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { PresetsEditor } from './PresetsEditor';
import { newPresetRow, type PresetRow } from './permissionForm';

function renderPresets(rows: PresetRow[], error?: string) {
  const onChange = vi.fn();
  render(<PresetsEditor columns={['owner_id', 'tenant']} rows={rows} error={error} onChange={onChange} />);
  return onChange;
}

describe('PresetsEditor', () => {
  test('"Add preset" appends an empty row', () => {
    const onChange = renderPresets([]);
    fireEvent.click(screen.getByRole('button', { name: /Add preset/ }));
    const [rows] = onChange.mock.calls[0] as [PresetRow[]];
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ column: '', value: '' });
  });

  test('choosing a column and a value updates only that row', () => {
    const first = newPresetRow('owner_id', 'X-Excalibase-User-Id');
    const second = newPresetRow();
    const onChange = renderPresets([first, second]);

    fireEvent.change(screen.getByLabelText('Preset column 2'), { target: { value: 'tenant' } });
    expect(onChange).toHaveBeenLastCalledWith([first, { ...second, column: 'tenant' }]);

    fireEvent.change(screen.getByLabelText('Preset value 2'), { target: { value: 'acme' } });
    expect(onChange).toHaveBeenLastCalledWith([first, { ...second, value: 'acme' }]);
  });

  test('a row can be removed', () => {
    const first = newPresetRow('owner_id', 'X-Excalibase-User-Id');
    const second = newPresetRow('tenant', 'acme');
    const onChange = renderPresets([first, second]);
    fireEvent.click(screen.getByRole('button', { name: 'Remove preset 1' }));
    expect(onChange).toHaveBeenCalledWith([second]);
  });

  test('a validation error is shown', () => {
    renderPresets([newPresetRow()], 'Pick a column for every preset.');
    expect(screen.getByTestId('presets-error')).toHaveTextContent('Pick a column for every preset.');
  });
});
