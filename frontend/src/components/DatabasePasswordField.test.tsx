import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { DatabasePasswordField } from './DatabasePasswordField';

describe('DatabasePasswordField', () => {
  test('hints the length and that blank generates one', () => {
    render(<DatabasePasswordField id="pw" label="Database password" value="" onChange={() => undefined} />);
    expect(screen.getByLabelText('Database password')).toHaveAttribute('placeholder', 'Generate for me');
    expect(screen.getByText(/16 to 128 characters/)).toBeInTheDocument();
  });

  test('reports typing and shows an error in place of the hint', async () => {
    const onChange = vi.fn();
    const { rerender } = render(<DatabasePasswordField id="pw" label="Database password" value="" onChange={onChange} />);
    await userEvent.type(screen.getByLabelText('Database password'), 'a');
    expect(onChange).toHaveBeenCalledWith('a');
    rerender(<DatabasePasswordField id="pw" label="Database password" value="a" onChange={onChange} error="Too short" />);
    expect(screen.getByRole('alert')).toHaveTextContent('Too short');
    expect(screen.queryByText(/16 to 128 characters/)).not.toBeInTheDocument();
  });
});
