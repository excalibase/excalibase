import { NavLink, useLocation } from 'react-router-dom';
import { ExternalLink } from 'lucide-react';
import { cn } from '../../../utils/cn';
import { useAuthStore } from '../../../stores/auth-store';
import { useCurrentOrg } from '../../../hooks/useMemberOrgs';
import { ADMIN_CONSOLE, PLATFORM_ADMIN_ROLES, shellNav, type ShellNavItem } from './navV2';

const LINK = 'flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm font-medium transition-colors';

function Item({ item }: { readonly item: ShellNavItem }) {
  const Icon = item.icon;
  const location = useLocation();
  if (item.external) {
    return (
      <a href={item.to} target="_blank" rel="noreferrer" className={cn(LINK, 'text-text-secondary hover:bg-surface-hover hover:text-text-primary')}>
        <Icon className="w-[18px] h-[18px]" aria-hidden="true" /> {item.label} <ExternalLink className="w-3 h-3 ml-auto" aria-hidden="true" />
      </a>
    );
  }
  // Two org pages share one path and differ by their tab, so they match on the whole address.
  const tabbed = item.to.includes('?');
  return (
    <NavLink to={item.to} end={item.to === '/'}
      className={({ isActive }) => cn(LINK, (tabbed ? `${location.pathname}${location.search}` === item.to : isActive) ? 'bg-purple-500/10 text-purple-400' : 'text-text-secondary hover:bg-surface-hover hover:text-text-primary')}>
      <Icon className="w-[18px] h-[18px]" aria-hidden="true" /> {item.label}
    </NavLink>
  );
}

export function SideNav() {
  const role = useAuthStore((s) => s.user?.role);
  const { org } = useCurrentOrg();
  const { main, help } = shellNav(org?.id);
  const isAdmin = role !== undefined && PLATFORM_ADMIN_ROLES.has(role);
  return (
    <nav aria-label="Main" className="flex w-56 flex-shrink-0 flex-col gap-0.5 border-r border-border-primary bg-surface-card px-2 py-3">
      {main.map((item) => <Item key={item.key} item={item} />)}
      <hr className="my-2 border-border-primary" />
      {help.map((item) => <Item key={item.key} item={item} />)}
      {isAdmin && (
        <div className="mt-auto pt-3">
          <Item item={ADMIN_CONSOLE} />
          <p className="px-3 text-[11px] text-text-tertiary">Platform admins only</p>
        </div>
      )}
    </nav>
  );
}
