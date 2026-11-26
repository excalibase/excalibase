import { describe, expect, test, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import { AdminTabs } from './AdminTabs';

const tabs = [{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }, { id: 'c', label: 'C', startsGroup: true }];

describe('AdminTabs', () => {
  test('is one row that scrolls sideways and never wraps', () => {
    render(<AdminTabs tabs={tabs} active="a" onChange={() => {}} />);
    const list = screen.getByRole('tablist');
    expect(list).toHaveClass('flex-nowrap', 'overflow-x-auto');
    expect(list).not.toHaveClass('flex-wrap');
    for (const tab of screen.getAllByRole('tab')) expect(tab).toHaveClass('whitespace-nowrap', 'shrink-0');
  });

  test('draws a separator only before a tab that starts a group', () => {
    render(<AdminTabs tabs={tabs} active="a" onChange={() => {}} />);
    const separators = screen.getAllByRole('separator');
    expect(separators).toHaveLength(1);
    expect(separators[0].nextElementSibling).toBe(screen.getByRole('tab', { name: 'C' }));
  });

  test('brings the active tab into view when it changes', () => {
    const scroll = vi.fn();
    Element.prototype.scrollIntoView = scroll;
    const { rerender } = render(<AdminTabs tabs={tabs} active="a" onChange={() => {}} />);
    scroll.mockClear();
    rerender(<AdminTabs tabs={tabs} active="c" onChange={() => {}} />);
    expect(scroll).toHaveBeenCalledTimes(1);
    expect(scroll.mock.contexts[0]).toBe(screen.getByRole('tab', { name: 'C' }));
  });
});
