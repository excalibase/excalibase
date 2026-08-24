import { useState, useEffect } from 'react';
import { useNavigate, useParams } from 'react-router-dom';
import { Command } from 'cmdk';
import {
  Home, Terminal, GitBranch, Database, Table2, Code, Package,
  Users, Shield, BarChart2, Zap, Bell, HardDrive, Camera,
  ArrowUpDown, Settings, Lock, KeyRound, Code2, Globe, Radio,
  Search,
} from 'lucide-react';
import { useTables } from '../../hooks/useSchema';

const PAGES = [
  { name: 'Home', icon: Home, path: '' },
  { name: 'SQL Editor', icon: Terminal, path: 'sql' },
  { name: 'Schema / ERD', icon: GitBranch, path: 'schema' },
  { name: 'Tables', icon: Table2, path: 'database/tables' },
  { name: 'Functions', icon: Code, path: 'database/functions' },
  { name: 'Extensions', icon: Package, path: 'database/extensions' },
  { name: 'Roles', icon: Users, path: 'database/roles' },
  { name: 'RLS Policies', icon: Shield, path: 'database/rls' },
  { name: 'Auth Users', icon: Lock, path: 'auth/users' },
  { name: 'Auth Sessions', icon: KeyRound, path: 'auth/sessions' },
  { name: 'Edge Functions', icon: Code2, path: 'edge-functions' },
  { name: 'API', icon: Globe, path: 'api' },
  { name: 'Realtime', icon: Radio, path: 'realtime' },
  { name: 'Metrics', icon: BarChart2, path: 'monitoring/metrics' },
  { name: 'Performance', icon: Zap, path: 'monitoring/performance' },
  { name: 'Alerts', icon: Bell, path: 'monitoring/alerts' },
  { name: 'Backups', icon: HardDrive, path: 'operations/backups' },
  { name: 'Snapshots', icon: Camera, path: 'operations/snapshots' },
  { name: 'Migrations', icon: ArrowUpDown, path: 'operations/migrations' },
  { name: 'Settings', icon: Settings, path: 'settings' },
];

export function CommandMenu() {
  const [open, setOpen] = useState(false);
  const navigate = useNavigate();
  const { projectId } = useParams<{ projectId: string }>();
  const { data: tables = [] } = useTables(projectId || '', 'public');

  useEffect(() => {
    const down = (e: KeyboardEvent) => {
      if (e.key === 'k' && (e.metaKey || e.ctrlKey)) {
        e.preventDefault();
        setOpen(o => !o);
      }
    };
    document.addEventListener('keydown', down);
    return () => document.removeEventListener('keydown', down);
  }, []);

  const go = (path: string) => {
    if (projectId) {
      navigate(`/project/${projectId}/${path}`);
    } else {
      navigate(path);
    }
    setOpen(false);
  };

  if (!open) return null;

  return (
    <>
      <button
        type="button"
        className="fixed inset-0 bg-black/50 z-[60] cursor-default border-0 p-0"
        onClick={() => setOpen(false)}
        aria-label="Close command menu"
      />
      <div className="fixed inset-0 z-[60] flex items-start justify-center pt-[20vh]" data-testid="command-menu">
        <Command className="w-full max-w-lg bg-surface-card border border-border-primary rounded-xl shadow-2xl overflow-hidden">
          <div className="flex items-center gap-2 px-4 border-b border-border-primary">
            <Search className="w-4 h-4 text-text-tertiary flex-shrink-0" />
            <Command.Input
              placeholder="Search pages, tables..."
              className="w-full py-3 bg-transparent text-sm text-text-primary placeholder:text-text-tertiary outline-none"
              autoFocus
            />
          </div>
          <Command.List className="max-h-72 overflow-y-auto p-2">
            <Command.Empty className="py-6 text-center text-sm text-text-tertiary">
              No results found.
            </Command.Empty>

            {projectId && (
              <>
                <Command.Group heading="Navigation" className="text-[10px] uppercase tracking-wider text-text-tertiary px-2 py-1.5">
                  {PAGES.map(({ name, icon: Icon, path }) => (
                    <Command.Item
                      key={path}
                      value={name}
                      onSelect={() => go(path)}
                      className="flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-text-secondary cursor-pointer data-[selected=true]:bg-purple-500/10 data-[selected=true]:text-purple-400"
                    >
                      <Icon className="w-4 h-4" />
                      {name}
                    </Command.Item>
                  ))}
                </Command.Group>

                {tables.length > 0 && (
                  <Command.Group heading="Tables" className="text-[10px] uppercase tracking-wider text-text-tertiary px-2 py-1.5">
                    {tables.map(t => (
                      <Command.Item
                        key={t.name}
                        value={`table ${t.name}`}
                        onSelect={() => go('database/tables')}
                        className="flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-text-secondary cursor-pointer data-[selected=true]:bg-purple-500/10 data-[selected=true]:text-purple-400"
                      >
                        <Database className="w-4 h-4" />
                        {t.name}
                      </Command.Item>
                    ))}
                  </Command.Group>
                )}
              </>
            )}

            <Command.Group heading="Platform" className="text-[10px] uppercase tracking-wider text-text-tertiary px-2 py-1.5">
              <Command.Item
                value="Dashboard"
                onSelect={() => { navigate('/'); setOpen(false); }}
                className="flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-text-secondary cursor-pointer data-[selected=true]:bg-purple-500/10 data-[selected=true]:text-purple-400"
              >
                <Home className="w-4 h-4" />
                Dashboard
              </Command.Item>
              <Command.Item
                value="Projects"
                onSelect={() => { navigate('/instances'); setOpen(false); }}
                className="flex items-center gap-2.5 px-3 py-2 rounded-lg text-sm text-text-secondary cursor-pointer data-[selected=true]:bg-purple-500/10 data-[selected=true]:text-purple-400"
              >
                <Database className="w-4 h-4" />
                Projects
              </Command.Item>
            </Command.Group>
          </Command.List>
          <div className="flex items-center justify-between px-4 py-2 border-t border-border-primary text-[10px] text-text-tertiary">
            <span>Navigate with ↑↓ • Select with ↵</span>
            <span>ESC to close</span>
          </div>
        </Command>
      </div>
    </>
  );
}
