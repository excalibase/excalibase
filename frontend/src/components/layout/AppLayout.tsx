import { useEffect, useState } from 'react';
import { NavLink, Outlet, useLocation } from 'react-router-dom';
import {
  LayoutDashboard,
  Database,
  DatabaseZap,
  FolderKanban,
  BarChart2,
  HardDrive,
  Camera,
  Zap,
  ArrowUpDown,
  Bell,
  Sun,
  Moon,
  Menu,
  X,
  ChevronDown,
  LogOut,
  type LucideIcon,
} from 'lucide-react';
import { useAuthStore } from '../../stores/auth-store';
import { cn } from '../../utils/cn';
import { useInstanceContext, InstanceProvider } from '../../context/InstanceContext';
import { useInstances } from '../../hooks/useProvisioning';

const TOP_NAV = [
  { icon: LayoutDashboard, label: 'Dashboard',    to: '/'          },
  { icon: Database,        label: 'Instances',     to: '/instances' },
  { icon: DatabaseZap,     label: 'Provision New', to: '/provision' },
  { icon: FolderKanban,    label: 'Projects',      to: '/projects'  },
];

const SCOPED_NAV = [
  { icon: BarChart2,   label: 'Metrics',      to: '/metrics'      },
  { icon: HardDrive,   label: 'Backups',      to: '/backups'      },
  { icon: Camera,      label: 'Snapshots',    to: '/snapshots'    },
  { icon: Zap,         label: 'Performance',  to: '/performance'  },
  { icon: ArrowUpDown, label: 'Migrations',   to: '/migrations'   },
  { icon: Bell,        label: 'Alerts',       to: '/alerts'       },
];

const ALL_NAV = [...TOP_NAV, ...SCOPED_NAV];

function InstancePicker() {
  const { projectId, setProjectId } = useInstanceContext();
  const { data: instances = [] } = useInstances();

  if (instances.length === 0) {
    return (
      <div className="mx-3 px-3 py-2 rounded-lg border border-border-primary bg-bg-secondary text-xs text-text-tertiary">
        No instances
      </div>
    );
  }

  return (
    <div className="mx-3 relative">
      <div className="relative">
        <select
          value={projectId}
          onChange={(e) => setProjectId(e.target.value)}
          className="w-full appearance-none pl-3 pr-8 py-2 bg-purple-500/10 border border-purple-500/30 rounded-lg text-purple-400 text-xs font-medium focus:outline-none focus:ring-2 focus:ring-purple-500 cursor-pointer"
        >
          {instances.map((i) => (
            <option key={i.projectId} value={i.projectId}>
              {i.projectId}
            </option>
          ))}
        </select>
        <ChevronDown className="absolute right-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-purple-400 pointer-events-none" />
      </div>
    </div>
  );
}

function NavItem({ icon: Icon, label, to }: { readonly icon: LucideIcon; readonly label: string; readonly to: string }) {
  return (
    <NavLink
      to={to}
      end={to === '/'}
      className={({ isActive }) =>
        cn(
          'flex items-center gap-3 px-3 py-2.5 rounded-lg text-sm font-medium transition-colors',
          isActive
            ? 'bg-purple-500/10 text-purple-400'
            : 'text-text-secondary hover:bg-surface-hover hover:text-text-primary'
        )
      }
    >
      <Icon className="w-5 h-5 flex-shrink-0" />
      {label}
    </NavLink>
  );
}

function AppLayoutInner() {
  const [dark, setDark] = useState(() => {
    const saved = localStorage.getItem('theme');
    if (saved) return saved === 'dark';
    return true;
  });
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const location = useLocation();
  const { user, logout } = useAuthStore();

  // Apply theme on mount
  useEffect(() => {
    document.documentElement.classList.toggle('dark', dark);
  }, [dark]);

  const toggleDark = () => {
    const next = !dark;
    setDark(next);
    document.documentElement.classList.toggle('dark', next);
    localStorage.setItem('theme', next ? 'dark' : 'light');
  };

  const currentPage = ALL_NAV.find((item) =>
    item.to === '/'
      ? location.pathname === '/'
      : location.pathname.startsWith(item.to)
  )?.label ?? 'Excalibase';

  return (
    <div className="flex h-screen bg-bg-primary overflow-hidden">
      {sidebarOpen && (
        <button
          type="button"
          className="fixed inset-0 bg-black/50 z-20 lg:hidden cursor-default"
          onClick={() => setSidebarOpen(false)}
          aria-label="Close sidebar"
        />
      )}

      {/* Sidebar */}
      <aside
        className={cn(
          'fixed lg:static inset-y-0 left-0 z-30 w-64 flex flex-col bg-surface-card border-r border-border-primary transition-transform duration-200',
          sidebarOpen ? 'translate-x-0' : '-translate-x-full lg:translate-x-0'
        )}
      >
        {/* Logo */}
        <div className="flex items-center gap-3 px-5 py-4 border-b border-border-primary">
          <img src="/logo-icon.png" alt="" className="w-8 h-8 object-contain flex-shrink-0" />
          <span className="font-bold text-text-primary text-lg">Excalibase</span>
          <button
            className="ml-auto lg:hidden text-text-secondary hover:text-text-primary"
            onClick={() => setSidebarOpen(false)}
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        {/* Nav */}
        <nav className="flex-1 px-3 py-4 space-y-1 overflow-y-auto">
          {/* Top group — no instance needed */}
          {TOP_NAV.map(({ icon, label, to }) => (
            <NavItem key={to} icon={icon} label={label} to={to} />
          ))}

          {/* Divider + instance picker */}
          <div className="pt-3 pb-1">
            <p className="px-3 mb-2 text-[10px] font-semibold uppercase tracking-widest text-text-tertiary">
              Instance
            </p>
            <InstancePicker />
          </div>

          {/* Scoped group — uses selected instance */}
          <div className="pt-1 space-y-1">
            {SCOPED_NAV.map(({ icon, label, to }) => (
              <NavItem key={to} icon={icon} label={label} to={to} />
            ))}
          </div>
        </nav>

        {/* Footer */}
        <div className="px-6 py-4 border-t border-border-primary text-xs text-text-tertiary">
          v0.1.0-poc
        </div>
      </aside>

      {/* Main */}
      <div className="flex-1 flex flex-col min-w-0 overflow-hidden">
        <header className="flex items-center gap-4 px-6 py-4 bg-surface-card border-b border-border-primary flex-shrink-0">
          <button
            className="lg:hidden text-text-secondary hover:text-text-primary"
            onClick={() => setSidebarOpen(true)}
          >
            <Menu className="w-6 h-6" />
          </button>
          <h1 className="text-lg font-semibold text-text-primary">{currentPage}</h1>
          <div className="ml-auto flex items-center gap-3">
            {user && (
              <span className="text-sm text-text-secondary hidden sm:inline">
                {user.username}
              </span>
            )}
            <button
              onClick={toggleDark}
              className="p-2 rounded-lg text-text-secondary hover:text-text-primary hover:bg-surface-hover transition-colors"
              title="Toggle dark mode"
            >
              {dark ? <Sun className="w-5 h-5" /> : <Moon className="w-5 h-5" />}
            </button>
            <button
              onClick={() => { void logout(); }}
              className="p-2 rounded-lg text-text-secondary hover:text-red-400 hover:bg-surface-hover transition-colors"
              title="Sign out"
            >
              <LogOut className="w-5 h-5" />
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

export function AppLayout() {
  return (
    <InstanceProvider>
      <AppLayoutInner />
    </InstanceProvider>
  );
}
