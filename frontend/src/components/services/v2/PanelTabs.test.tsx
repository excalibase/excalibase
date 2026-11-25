import { describe, test, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { PanelTabs } from './PanelTabs';

const TABS = [
  { key: 'overview', label: 'Overview' },
  { key: 'connect', label: 'Connect' },
  { key: 'data', label: 'Data' },
];

function setup(active = 'overview') {
  const onSelect = vi.fn();
  render(
    <PanelTabs label="Database" tabs={TABS} active={active} onSelect={onSelect} idPrefix="p">
      <p>body</p>
    </PanelTabs>,
  );
  return onSelect;
}

describe('PanelTabs', () => {
  test('is a tablist with the active tab selected and the others out of the tab order', () => {
    setup('connect');
    expect(screen.getByRole('tablist', { name: 'Database' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Connect' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tab', { name: 'Connect' })).toHaveAttribute('tabindex', '0');
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute('aria-selected', 'false');
    expect(screen.getByRole('tab', { name: 'Overview' })).toHaveAttribute('tabindex', '-1');
  });

  test('the body is a tabpanel labelled by the active tab', () => {
    setup('data');
    const panel = screen.getByRole('tabpanel');
    expect(panel).toHaveAccessibleName('Data');
    expect(screen.getByRole('tab', { name: 'Data' })).toHaveAttribute('aria-controls', panel.id);
  });

  test('arrow keys move focus and select, wrapping at the ends', async () => {
    const onSelect = setup('overview');
    screen.getByRole('tab', { name: 'Overview' }).focus();
    await userEvent.keyboard('{ArrowRight}');
    expect(onSelect).toHaveBeenLastCalledWith('connect');
    screen.getByRole('tab', { name: 'Overview' }).focus();
    await userEvent.keyboard('{ArrowLeft}');
    expect(onSelect).toHaveBeenLastCalledWith('data');
  });

  test('Home and End jump to the first and last tab', async () => {
    const onSelect = setup('connect');
    screen.getByRole('tab', { name: 'Connect' }).focus();
    await userEvent.keyboard('{End}');
    expect(onSelect).toHaveBeenLastCalledWith('data');
    await userEvent.keyboard('{Home}');
    expect(onSelect).toHaveBeenLastCalledWith('overview');
  });

  test('a click selects', async () => {
    const onSelect = setup();
    await userEvent.click(screen.getByRole('tab', { name: 'Data' }));
    expect(onSelect).toHaveBeenCalledWith('data');
  });
});
