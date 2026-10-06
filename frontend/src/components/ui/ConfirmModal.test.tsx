import { describe, test, expect, vi } from 'vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { ConfirmModal } from './ConfirmModal';

type ModalProps = Parameters<typeof ConfirmModal>[0];

function mount(props: Partial<ModalProps> = {}) {
  const onClose = vi.fn();
  const onConfirm = vi.fn();
  const view = render(
    <ConfirmModal open onClose={onClose} onConfirm={onConfirm} title="Drop table" message="This cannot be undone." {...props} />,
  );
  return { onClose, onConfirm, view };
}

describe('ConfirmModal', () => {
  test('renders nothing while closed', () => {
    mount({ open: false });
    expect(screen.queryByTestId('confirm-modal')).not.toBeInTheDocument();
  });

  test('a plain confirmation confirms with its label', () => {
    const { onConfirm } = mount({ confirmLabel: 'Drop' });
    expect(screen.getByText('This cannot be undone.')).toBeInTheDocument();
    fireEvent.click(screen.getByTestId('modal-confirm'));
    expect(screen.getByTestId('modal-confirm')).toHaveTextContent('Drop');
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  test('a destructive confirmation needs the exact text typed', () => {
    const { onConfirm } = mount({ confirmText: 'orders', destructive: true });
    const confirm = screen.getByTestId('modal-confirm');
    expect(confirm).toBeDisabled();
    fireEvent.change(screen.getByTestId('confirm-input'), { target: { value: 'order' } });
    expect(confirm).toBeDisabled();
    fireEvent.change(screen.getByTestId('confirm-input'), { target: { value: 'orders ' } });
    expect(confirm).toBeDisabled();
    fireEvent.change(screen.getByTestId('confirm-input'), { target: { value: 'orders' } });
    fireEvent.click(confirm);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId('confirm-input')).toHaveValue('');
  });

  test('while the action runs the button says so and does not confirm again', () => {
    const { onConfirm } = mount({ loading: true });
    expect(screen.getByTestId('modal-confirm')).toHaveTextContent('Processing...');
    fireEvent.click(screen.getByTestId('modal-confirm'));
    expect(onConfirm).not.toHaveBeenCalled();
  });

  test('an action with no loading state confirms once, however fast the clicks', () => {
    const { onConfirm } = mount();
    const confirm = screen.getByTestId('modal-confirm');
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(confirm).toBeDisabled();
  });

  test('an action that returns a promise can confirm again once it settles', async () => {
    let finish: () => void = () => {};
    const onConfirm = vi.fn(() => new Promise<void>((resolve) => { finish = resolve; }));
    mount({ onConfirm });
    const confirm = screen.getByTestId('modal-confirm');
    fireEvent.click(confirm);
    fireEvent.click(confirm);
    expect(onConfirm).toHaveBeenCalledTimes(1);
    expect(confirm).toBeDisabled();
    await act(async () => finish());
    expect(confirm).toBeEnabled();
  });

  test('a loading state that ends lets the user confirm again', () => {
    const { onConfirm, view } = mount();
    fireEvent.click(screen.getByTestId('modal-confirm'));
    const props = { open: true, onClose: vi.fn(), onConfirm, title: 'Drop table', message: 'This cannot be undone.' };
    view.rerender(<ConfirmModal {...props} loading />);
    view.rerender(<ConfirmModal {...props} loading={false} />);
    fireEvent.click(screen.getByTestId('modal-confirm'));
    expect(onConfirm).toHaveBeenCalledTimes(2);
  });

  test('reopening the dialog lets it confirm again', () => {
    const { onConfirm, view } = mount();
    fireEvent.click(screen.getByTestId('modal-confirm'));
    const props = { onClose: vi.fn(), onConfirm, title: 'Drop table', message: 'This cannot be undone.' };
    view.rerender(<ConfirmModal {...props} open={false} />);
    view.rerender(<ConfirmModal {...props} open />);
    fireEvent.click(screen.getByTestId('modal-confirm'));
    expect(onConfirm).toHaveBeenCalledTimes(2);
  });

  test('cancel, the close button and the backdrop all close and clear the typed text', () => {
    const { onClose } = mount({ confirmText: 'orders' });
    fireEvent.change(screen.getByTestId('confirm-input'), { target: { value: 'ord' } });
    fireEvent.click(screen.getByTestId('modal-cancel'));
    expect(screen.getByTestId('confirm-input')).toHaveValue('');
    fireEvent.click(screen.getByTestId('modal-close'));
    fireEvent.click(screen.getByTestId('modal-backdrop'));
    expect(onClose).toHaveBeenCalledTimes(3);
  });
});
