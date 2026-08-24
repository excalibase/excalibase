import { useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Database, Loader2, FolderOpen } from 'lucide-react';
import { api } from '../api/client';
import type { DatabaseInstance } from '../types';
import { cn } from '../utils/cn';

const statusColors: Record<string, string> = {
  ACTIVE: 'bg-emerald-500/10 text-emerald-400 border-emerald-500/30',
  PROVISIONING: 'bg-amber-500/10 text-amber-400 border-amber-500/30',
  FAILED: 'bg-red-500/10 text-red-400 border-red-500/30',
};

const dbTypeColors: Record<string, string> = {
  POSTGRESQL: 'text-blue-400',
  MYSQL: 'text-orange-400',
  MONGODB: 'text-green-400',
};

export function ProjectsPage() {
  const navigate = useNavigate();

  const { data: projects, isLoading, error } = useQuery({
    queryKey: ['projects'],
    queryFn: async () => {
      const response = await api.get<DatabaseInstance[]>('/provision');
      return response.data;
    },
  });

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-64">
        <Loader2 className="w-8 h-8 animate-spin text-purple-400" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="text-center py-16 text-red-400">
        Failed to load projects. Please try again.
      </div>
    );
  }

  if (!projects || projects.length === 0) {
    return (
      <div className="text-center py-16">
        <FolderOpen className="w-12 h-12 text-text-tertiary mx-auto mb-3" />
        <p className="text-text-secondary">No projects found</p>
        <p className="text-sm text-text-tertiary mt-1">Provision a database to get started</p>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="text-xl font-semibold text-text-primary">Projects</h2>
        <p className="text-sm text-text-secondary mt-1">Manage your database projects</p>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
        {projects.map((project) => (
          <button
            key={project.projectId}
            onClick={() => navigate(`/project/${project.projectId}`)}
            className="bg-surface-card border border-border-primary rounded-xl p-5 text-left hover:border-purple-500/50 hover:bg-surface-hover transition-all group"
          >
            <div className="flex items-start justify-between mb-3">
              <div className="flex items-center gap-2">
                <Database className={cn('w-5 h-5', dbTypeColors[project.databaseType] || 'text-text-secondary')} />
                <span className="font-semibold text-text-primary group-hover:text-purple-400 transition-colors">
                  {project.projectId}
                </span>
              </div>
              <span
                className={cn(
                  'text-xs px-2 py-0.5 rounded-full border',
                  statusColors[project.status] || 'bg-bg-tertiary text-text-secondary border-border-primary'
                )}
              >
                {project.status}
              </span>
            </div>

            <div className="space-y-1.5 text-sm">
              <div className="flex justify-between">
                <span className="text-text-tertiary">Type</span>
                <span className="text-text-secondary">{project.databaseType}</span>
              </div>
              <div className="flex justify-between">
                <span className="text-text-tertiary">Tier</span>
                <span className="text-text-secondary">{project.tier}</span>
              </div>
              <div className="flex justify-between">
                <span className="text-text-tertiary">Namespace</span>
                <span className="text-text-secondary truncate ml-4">{project.namespace}</span>
              </div>
            </div>
          </button>
        ))}
      </div>
    </div>
  );
}
