import { describe, test, expect } from 'vitest';
import { useState } from 'react';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { NewPasswordFields, passwordProblem, newPasswordReady } from './NewPasswordFields';

const GOOD = ['Studio', '9', 'pass'].join('');

function Harness({ label = 'New password' }: { readonly label?: string }) {
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  return (
    <>
      <NewPasswordFields id="pw" label={label} password={password} confirm={confirm}
        onPasswordChange={setPassword} onConfirmChange={setConfirm} />
      <button type="submit" disabled={!newPasswordReady(password, confirm)}>Save</button>
    </>
  );
}

describe('password rules', () => {
  test.each([
    ['', 'Min 8 characters'],
    ['Ab1', 'Min 8 characters'],
    ['abcdefg1', 'Mixed case + at least one digit'],
    ['ABCDEFG1', 'Mixed case + at least one digit'],
    ['Abcdefgh', 'Mixed case + at least one digit'],
    [GOOD, undefined],
  ])('%j -> %s', (value, problem) => {
    expect(passwordProblem(value)).toBe(problem);
  });

  test('ready only when the rules hold and both entries match', () => {
    expect(newPasswordReady(GOOD, GOOD)).toBe(true);
    expect(newPasswordReady(GOOD, `${GOOD}x`)).toBe(false);
    expect(newPasswordReady('short', 'short')).toBe(false);
  });
});

describe('NewPasswordFields', () => {
  test('both fields are hidden new-password inputs', () => {
    render(<Harness />);
    for (const name of ['New password', 'Confirm new password']) {
      const input = screen.getByLabelText(name);
      expect(input).toHaveAttribute('type', 'password');
      expect(input).toHaveAttribute('autocomplete', 'new-password');
    }
  });

  test('a mismatch is reported inline and blocks submit', async () => {
    const u = userEvent.setup();
    render(<Harness />);
    await u.type(screen.getByLabelText('New password'), GOOD);
    await u.type(screen.getByLabelText('Confirm new password'), `${GOOD}x`);

    expect(screen.getByText("Passwords don't match")).toBeInTheDocument();
    expect(screen.getByLabelText('Confirm new password')).toHaveAttribute('aria-invalid', 'true');
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  });

  test('matching entries that meet the rules enable submit', async () => {
    const u = userEvent.setup();
    render(<Harness />);
    await u.type(screen.getByLabelText('New password'), GOOD);
    await u.type(screen.getByLabelText('Confirm new password'), GOOD);

    expect(screen.queryByText("Passwords don't match")).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled();
  });

  test('a weak password that matches still blocks submit and says why', async () => {
    const u = userEvent.setup();
    render(<Harness />);
    await u.type(screen.getByLabelText('New password'), 'weakpass');
    await u.type(screen.getByLabelText('Confirm new password'), 'weakpass');

    expect(screen.getByText('Mixed case + at least one digit')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  });

  test('one toggle shows and hides both fields', async () => {
    const u = userEvent.setup();
    render(<Harness />);
    const toggle = screen.getByRole('button', { name: 'Show passwords' });
    expect(toggle).toHaveAttribute('aria-pressed', 'false');

    await u.click(toggle);
    expect(toggle).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByLabelText('New password')).toHaveAttribute('type', 'text');
    expect(screen.getByLabelText('Confirm new password')).toHaveAttribute('type', 'text');

    await u.click(toggle);
    expect(toggle).toHaveAttribute('aria-pressed', 'false');
    expect(screen.getByLabelText('New password')).toHaveAttribute('type', 'password');
    expect(screen.getByLabelText('Confirm new password')).toHaveAttribute('type', 'password');
  });

  test('the confirm label follows the field label', () => {
    render(<Harness label="Password" />);
    expect(screen.getByLabelText('Password')).toBeInTheDocument();
    expect(screen.getByLabelText('Confirm password')).toBeInTheDocument();
  });
});
