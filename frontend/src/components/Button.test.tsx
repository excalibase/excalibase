import { describe, test, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { Button } from './Button';

// EXC-553: a disabled button must look disabled, not like one that does nothing.
describe('Button', () => {
  test('a disabled button is dimmed and shows it cannot be pressed', () => {
    render(<Button disabled>Go</Button>);
    const button = screen.getByRole('button', { name: 'Go' });
    expect(button).toBeDisabled();
    expect(button).toHaveClass('disabled:opacity-50', 'disabled:cursor-not-allowed');
  });
});
