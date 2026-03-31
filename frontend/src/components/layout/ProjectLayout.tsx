import { NavLink, Outlet, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import {
  Table2,
  Terminal,
  FunctionSquare,
  Shield,
  HardDrive,
  Users,
  Puzzle,
  Settings,
  Loader2,
  ArrowLeft,
} from 'lucide-react';
import { api } from '../../api/client';
import type { DatabaseInstance } from '../../types';
import { cn } from '../../utils/cn';

const TABS = [
  { icon: Table2, label: 'Schema', to: 'schema' },
  { icon: Terminal, label: 'SQL Editor', to: 'sql' },
  { icon: FunctionSquare, label: 'Functions', to: 'functions' },
  { icon: Shield, label: 'RLS', to: 'rls' },
  { icon: HardDrive, label: 'Backups', to: 'backups' },
  { icon: Users, label: 'Roles', to: 'roles' },
  { icon: Puzzle, label: 'Extensions', to: 'extensions' },
  { icon: Settings, label: 'Settings', to: 'settings' },
];

export function ProjectLayout() {
  const { projectId } = useParams<{ projectId: string }>();

  const { data: project, isLoading, error } = useQuery({
    queryKey: ['project', projectId],
    queryFn: async () => {
      const response = await api.get<DatabaseInstance>(`/provision/${projectId}`);
      return response.data;
    },
    enabled: !!projectId,
  });

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-64">
        <Loader2 className="w-8 h-8 animate-spin text-purple-400" />
      </div>
    );
  }

  if (error || !project) {
    return (
      <div className="text-center py-16 text-red-400">
        Failed to load project. Please try again.
      </div>
    );
  }

  return (
    <div className="space-y-0 -m-6">
      {/* Project header */}
      <div className="px-6 pt-4 pb-0 border-b border-border-primary bg-surface-card">
        <div className="flex items-center gap-3 mb-4">
          <NavLink
            to="/projects"
            className="p-1.5 rounded-lg text-text-tertiary hover:text-text-primary hover:bg-surface-hover transition-colors"
          >
            <ArrowLeft className="w-4 h-4" />
          </NavLink>
          <div>
            <h2 className="text-lg font-semibold text-text-primary">{project.projectId}</h2>
            <p className="text-xs text-text-tertiary">
              {project.databaseType} / {project.tier} / {project.namespace}
            </p>
          </div>
        </div>

        {/* Tab navigation */}
        <nav className="flex gap-0 -mb-px overflow-x-auto">
          {TABS.map(({ icon: Icon, label, to }) => (
            <NavLink
              key={to}
              to={to}
              className={({ isActive }) =>
                cn(
                  'flex items-center gap-2 px-4 py-2.5 text-sm font-medium border-b-2 whitespace-nowrap transition-colors',
                  isActive
                    ? 'border-purple-500 text-purple-400'
                    : 'border-transparent text-text-secondary hover:text-text-primary hover:border-border-secondary'
                )
              }
            >
              <Icon className="w-4 h-4" />
              {label}
            </NavLink>
          ))}
        </nav>
      </div>

      {/* Tab content */}
      <div className="p-6">
        <Outlet />
      </div>
    </div>
  );
}
