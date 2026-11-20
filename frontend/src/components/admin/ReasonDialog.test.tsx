import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ReasonDialog } from './ReasonDialog';

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
