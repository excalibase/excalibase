import { describe, expect, test, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { AdminConsoleV2, adminTabs, initialTab } from './AdminConsoleV2';

vi.mock('../NeedsAttention', () => ({ NeedsAttention: () => <div>attention</div> }));
vi.mock('./ProjectsAdminTable', () => ({ ProjectsAdminTable: () => <div>projects list</div> }));
vi.mock('./UsersAdminTable', () => ({ UsersAdminTable: () => <div>users list</div> }));
vi.mock('./OrgsAdminTable', () => ({ OrgsAdminTable: () => <div>orgs list</div> }));

describe('which tabs an admin sees', () => {
  test('a platform admin sees every section', () => {
    expect(adminTabs(true).map((t) => t.id)).toEqual(['attention', 'projects', 'users', 'orgs', 'platform']);
  });

  test('an operator sees projects and the platform cards, never the people lists', () => {
    expect(adminTabs(false).map((t) => t.id)).toEqual(['projects', 'platform']);
  });

  test('an opened project lands on Projects, a tab asked for wins, an unknown one is ignored', () => {
    const tabs = adminTabs(true);
    expect(initialTab(tabs, null, 'p1')).toBe('projects');
    expect(initialTab(tabs, 'users', 'p1')).toBe('users');
    expect(initialTab(tabs, 'nope', null)).toBe('attention');
    expect(initialTab(adminTabs(false), 'users', null)).toBe('projects');
  });
});

function renderConsole(canMutate: boolean, path = '/admin') {
  return render(<MemoryRouter initialEntries={[path]}><AdminConsoleV2 canMutate={canMutate} platform={<div>platform cards</div>} /></MemoryRouter>);
}

describe('AdminConsoleV2', () => {
  test('shows one list at a time and switches with a click', async () => {
    renderConsole(true);
    expect(screen.getByText('attention')).toBeInTheDocument();
    expect(screen.queryByText('projects list')).toBeNull();
    await userEvent.click(screen.getByRole('tab', { name: 'Projects' }));
    expect(screen.getByText('projects list')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Projects' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tabpanel')).toHaveAttribute('aria-labelledby', 'admin-tab-projects');
  });

  test('arrow keys move between tabs', async () => {
    renderConsole(true);
    screen.getByRole('tab', { name: 'Needs attention' }).focus();
    await userEvent.keyboard('{ArrowRight}');
    expect(screen.getByRole('tab', { name: 'Projects' })).toHaveFocus();
    expect(screen.getByText('projects list')).toBeInTheDocument();
    await userEvent.keyboard('{ArrowLeft}{ArrowLeft}');
    expect(screen.getByRole('tab', { name: 'Platform' })).toHaveFocus();
  });

  test('an operator is never shown the people lists', () => {
    renderConsole(false, '/admin?tab=users');
    expect(screen.queryByText('users list')).toBeNull();
    expect(screen.getByText('projects list')).toBeInTheDocument();
  });
});
