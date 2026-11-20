import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { SecretChecklist } from './SecretChecklist';

describe('SecretChecklist', () => {
  test('ticks follow the rows, and a click reports the name and the new state', async () => {
    const onToggle = vi.fn();
    render(
      <SecretChecklist
        rows={[
          { name: 'A_KEY', checked: true },
          { name: 'B_KEY', checked: false },
        ]}
        pending={false}
        error={null}
        testIdPrefix="pick"
        onToggle={onToggle}
      />,
    );
    expect(screen.getByTestId('pick-A_KEY')).toBeChecked();
    expect(screen.getByTestId('pick-B_KEY')).not.toBeChecked();
    await userEvent.click(screen.getByTestId('pick-B_KEY'));
    expect(onToggle).toHaveBeenCalledWith('B_KEY', true);
  });

  test('while saving the boxes are disabled, and a failure is shown', () => {
    render(
      <SecretChecklist
        rows={[{ name: 'A_KEY', checked: false }]}
        pending
        error={{ response: { data: { error: 'no such secret' } } }}
        testIdPrefix="pick"
        onToggle={vi.fn()}
      />,
    );
    expect(screen.getByTestId('pick-A_KEY')).toBeDisabled();
    expect(screen.getByRole('alert')).toHaveTextContent('no such secret');
  });
});
