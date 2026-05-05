import { Outlet, NavLink } from 'react-router-dom';
import { LayoutDashboard, Database, DatabaseZap, Building2, ShieldCheck, Sun, Moon, LogOut } from 'lucide-react';
import { cn } from '../../utils/cn';
import { useDarkMode } from '../../hooks/useDarkMode';
import { useAuthStore } from '../../stores/auth-store';
import { useDeploymentMode, isSelfHosted } from '../../hooks/useDeploymentMode';

const PLATFORM_ADMIN_ROLES = new Set(['platform_admin', 'platform_operator']);

const ALL_NAV = [
  { icon: LayoutDashboard, label: 'Dashboard', to: '/', cloudOnly: false, adminOnly: false },
  { icon: Building2, label: 'Organizations', to: '/orgs', cloudOnly: true, adminOnly: false },
  { icon: Database, label: 'Projects', to: '/instances', cloudOnly: false, adminOnly: false },
  { icon: DatabaseZap, label: 'Provision', to: '/provision', cloudOnly: false, adminOnly: false },
  { icon: ShieldCheck, label: 'Platform Admin', to: '/admin', cloudOnly: false, adminOnly: true },
];

export function PlatformLayout() {
  const { dark, toggle } = useDarkMode();
  const { user, logout } = useAuthStore();
  const mode = useDeploymentMode();

  const isAdmin = user ? PLATFORM_ADMIN_ROLES.has(user.role) : false;
  const NAV = ALL_NAV.filter((item) => {
    if (item.cloudOnly && isSelfHosted(mode)) return false;
    if (item.adminOnly && !isAdmin) return false;
    return true;
  });

  return (
    <div className="flex h-screen bg-bg-primary overflow-hidden">
      <aside className="w-56 flex flex-col bg-surface-card border-r border-border-primary">
        <div className="flex items-center gap-2.5 px-4 py-4 border-b border-border-primary">
          <img src="/logo-icon.png" alt="" className="w-7 h-7 object-contain" />
          <span className="font-bold text-text-primary text-base">Excalibase</span>
        </div>
        <nav className="flex-1 px-2 py-3 space-y-0.5" data-testid="platform-nav">
          {NAV.map(({ icon: Icon, label, to }) => (
            <NavLink
              key={to}
              to={to}
              end={to === '/'}
              className={({ isActive }) =>
                cn(
                  'flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm font-medium transition-colors',
                  isActive
                    ? 'bg-purple-500/10 text-purple-400'
                    : 'text-text-secondary hover:bg-surface-hover hover:text-text-primary'
                )
              }
            >
              <Icon className="w-[18px] h-[18px]" />
              {label}
            </NavLink>
          ))}
        </nav>
        <div className="px-4 py-3 border-t border-border-primary text-[10px] text-text-tertiary">
          v0.1.0-poc
        </div>
      </aside>

      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        <header className="flex items-center h-12 px-6 bg-surface-card border-b border-border-primary flex-shrink-0">
          <h1 className="text-base font-semibold text-text-primary">Excalibase</h1>
          <div className="ml-auto flex items-center gap-2">
            {user && (
              <span className="text-sm text-text-secondary hidden sm:inline">{user.username}</span>
            )}
            <button
              onClick={toggle}
              className="p-2 rounded-lg text-text-secondary hover:text-text-primary hover:bg-surface-hover transition-colors"
            >
              {dark ? <Sun className="w-4 h-4" /> : <Moon className="w-4 h-4" />}
            </button>
            <button
              onClick={() => { void logout(); }}
              className="p-2 rounded-lg text-text-secondary hover:text-red-400 hover:bg-surface-hover transition-colors"
              title="Sign out"
            >
              <LogOut className="w-4 h-4" />
            </button>
          </div>
        </header>
        <main className="flex-1 overflow-y-auto p-6">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
