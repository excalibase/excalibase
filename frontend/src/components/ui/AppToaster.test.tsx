import { describe, test, expect, vi } from 'vitest';
import { act, render, screen } from '@testing-library/react';
import { toast } from 'sonner';
import { AppToaster } from './AppToaster';
import { SidePanel } from './SidePanel';

describe('AppToaster', () => {
  test('toasts stack on the side opposite the drawer, clear of its footer actions', async () => {
    render(
      <>
        <SidePanel
          open
          onClose={vi.fn()}
          title="Import data"
          footer={<button type="button">Import</button>}
        >
          body
        </SidePanel>
        <AppToaster />
      </>,
    );
    act(() => {
      toast.success('Row inserted');
    });
    const shown = await screen.findByText('Row inserted');
    const toaster = shown.closest('[data-sonner-toaster]');

    expect(toaster).toHaveAttribute('data-x-position', 'left');
    expect(toaster).toHaveAttribute('data-y-position', 'bottom');
    expect(screen.getByTestId('sidepanel')).toHaveClass('right-0');
  });
});
