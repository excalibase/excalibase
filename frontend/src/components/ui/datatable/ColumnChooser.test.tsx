import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ColumnChooser } from './ColumnChooser';

const COLUMNS = [
  { id: 'name', header: 'Name', cell: () => null, locked: true },
  { id: 'plan', header: 'Plan', cell: () => null },
];

describe('ColumnChooser', () => {
  test('lists columns, locks the required one, toggles the rest and closes on Escape', async () => {
    const onToggle = vi.fn();
    render(<ColumnChooser columns={COLUMNS} hidden={['plan']} onToggle={onToggle} />);
    await userEvent.click(screen.getByRole('button', { name: /columns/i }));
    expect(screen.getByRole('checkbox', { name: 'Name' })).toBeDisabled();
    expect(screen.getByRole('checkbox', { name: 'Plan' })).not.toBeChecked();
    await userEvent.click(screen.getByRole('checkbox', { name: 'Plan' }));
    expect(onToggle).toHaveBeenCalledWith('plan');
    await userEvent.keyboard('{Escape}');
    expect(screen.queryByRole('checkbox', { name: 'Name' })).not.toBeInTheDocument();
  });

  test('closes on an outside click', async () => {
    render(<div><span>outside</span><ColumnChooser columns={COLUMNS} hidden={[]} onToggle={vi.fn()} /></div>);
    await userEvent.click(screen.getByRole('button', { name: /columns/i }));
    await userEvent.click(screen.getByText('outside'));
    expect(screen.queryByRole('checkbox', { name: 'Name' })).not.toBeInTheDocument();
  });
});
