import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ReasonDialog } from './ReasonDialog';

describe('ReasonDialog minimum-length hint', () => {
  test('counts down the missing characters, is linked to the box, and disappears once enough is written', async () => {
    render(<ReasonDialog title="Change plan" confirmLabel="Change" onConfirm={() => {}} onCancel={() => {}} />);
    const box = screen.getByLabelText('Reason');
    const confirm = screen.getByRole('button', { name: 'Change' });

    expect(screen.getByText('At least 8 characters (8 more)')).toBeInTheDocument();
    await userEvent.type(box, 'Admin');
    const hint = screen.getByText('At least 8 characters (3 more)');
    expect(box).toHaveAttribute('aria-describedby', hint.id);
    expect(confirm).toBeDisabled();
    expect(confirm).toHaveAttribute('title', 'Write a reason of at least 8 characters');

    await userEvent.type(box, 'ist');
    expect(screen.queryByText(/At least 8 characters/)).not.toBeInTheDocument();
    expect(box).not.toHaveAttribute('aria-describedby', hint.id);
    expect(confirm).toBeEnabled();
    expect(confirm).not.toHaveAttribute('title');
  });

  test('spaces around the words do not count towards the minimum', async () => {
    render(<ReasonDialog title="t" confirmLabel="Go" onConfirm={() => {}} onCancel={() => {}} />);
    await userEvent.type(screen.getByLabelText('Reason'), '   ok  ');
    expect(screen.getByText('At least 8 characters (6 more)')).toBeInTheDocument();
  });
});

describe('ReasonDialog', () => {
  test('confirming is refused until a reason is written', async () => {
    const onConfirm = vi.fn();
    render(<ReasonDialog title="Suspend a@x.test" confirmLabel="Suspend" onConfirm={onConfirm} onCancel={() => {}} />);

    const confirm = screen.getByRole('button', { name: 'Suspend' });
    expect(confirm).toBeDisabled();

    await userEvent.type(screen.getByLabelText('Reason'), 'proxy abuse on the free tier');
    expect(confirm).toBeEnabled();
    await userEvent.click(confirm);

    expect(onConfirm).toHaveBeenCalledWith('proxy abuse on the free tier');
  });

  test('a blank or one-word reason does not count', async () => {
    render(<ReasonDialog title="t" confirmLabel="Go" onConfirm={() => {}} onCancel={() => {}} />);
    await userEvent.type(screen.getByLabelText('Reason'), '   ok  ');
    expect(screen.getByRole('button', { name: 'Go' })).toBeDisabled();
  });

  test('says why a request failed and can be cancelled', async () => {
    const onCancel = vi.fn();
    render(<ReasonDialog title="t" confirmLabel="Go" error="cluster busy" onConfirm={() => {}} onCancel={onCancel} />);
    expect(screen.getByRole('alert')).toHaveTextContent('cluster busy');
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(onCancel).toHaveBeenCalled();
  });
});
