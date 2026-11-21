import { NavLink, useLocation } from 'react-router-dom';
import { ExternalLink } from 'lucide-react';
import { cn } from '../../../utils/cn';
import { useAuthStore } from '../../../stores/auth-store';
import { useCurrentOrg } from '../../../hooks/useMemberOrgs';
import { ADMIN_CONSOLE, PLATFORM_ADMIN_ROLES, shellNav, type ShellNavItem } from './navV2';

const LINK = 'flex flex-shrink-0 items-center whitespace-nowrap gap-2.5 px-3 py-2 rounded-lg text-sm font-medium transition-colors';

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
      className={({ isActive }) => {
        const current = tabbed ? `${location.pathname}${location.search}` === item.to : isActive;
        return cn(LINK, current ? 'bg-purple-500/10 text-purple-400' : 'text-text-secondary hover:bg-surface-hover hover:text-text-primary');
      }}>
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
    <nav aria-label="Main" className="flex w-full flex-shrink-0 flex-row items-center gap-0.5 overflow-x-auto border-b border-border-primary bg-surface-card px-2 py-2 md:w-56 md:flex-col md:items-stretch md:overflow-x-visible md:border-b-0 md:border-r md:py-3">
      {main.map((item) => <Item key={item.key} item={item} />)}
      <hr className="my-2 hidden border-border-primary md:block" />
      {help.map((item) => <Item key={item.key} item={item} />)}
      {isAdmin && (
        <div className="md:mt-auto md:pt-3">
          <Item item={ADMIN_CONSOLE} />
          <p className="hidden px-3 text-[11px] text-text-tertiary md:block">Platform admins only</p>
        </div>
      )}
    </nav>
  );
}
