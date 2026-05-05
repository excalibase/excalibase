import { useNavigate } from 'react-router-dom';
import { useInstances } from '../hooks/useProvisioning';
import { StatusBadge } from '../components/shared/StatusBadge';
import { MetricCard } from '../components/shared/MetricCard';
import { Database, DatabaseZap, CheckCircle, Loader2, XCircle } from 'lucide-react';
import { Button } from '../components/Button';

export function DashboardPage() {
  const { data: instances = [], isLoading } = useInstances();
  const navigate = useNavigate();

  const total       = instances.length;
  const active      = instances.filter((i) => i.status === 'ACTIVE').length;
  const provisioning = instances.filter((i) => i.currentStage !== 'COMPLETED' && i.currentStage !== 'FAILED').length;
  const failed      = instances.filter((i) => i.currentStage === 'FAILED').length;

  return (
    <div className="space-y-8 max-w-7xl mx-auto">
      {/* Stat cards */}
      <div className="grid grid-cols-2 lg:grid-cols-4 gap-4">
        <MetricCard icon={<Database className="w-5 h-5" />}     label="Total Instances" value={total}        color="text-purple-400" />
        <MetricCard icon={<CheckCircle className="w-5 h-5" />} label="Active"          value={active}       color="text-green-400"   />
        <MetricCard icon={<Loader2 className="w-5 h-5" />}     label="Provisioning"    value={provisioning} color="text-blue-400"    />
        <MetricCard icon={<XCircle className="w-5 h-5" />}     label="Failed"          value={failed}       color="text-red-500"     />
      </div>

      {/* Recent instances */}
      <div className="bg-surface-card border border-border-primary rounded-xl">
        <div className="flex items-center justify-between px-6 py-4 border-b border-border-primary">
          <h2 className="font-semibold text-text-primary">Recent Instances</h2>
          <Button size="sm" onClick={() => navigate('/provision')}>
            <DatabaseZap className="w-4 h-4 mr-1.5" />
            Provision New
          </Button>
        </div>

        {isLoading && (
          <div className="flex items-center justify-center py-16">
            <Loader2 className="w-6 h-6 animate-spin text-accent-primary" />
          </div>
        )}
        {!isLoading && instances.length === 0 && (
          <div className="text-center py-16">
            <Database className="w-12 h-12 mx-auto mb-3 text-text-tertiary" />
            <p className="text-text-secondary">No instances yet.</p>
            <Button className="mt-4" onClick={() => navigate('/provision')}>Create your first database</Button>
          </div>
        )}
        {!isLoading && instances.length > 0 && (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-text-tertiary border-b border-border-primary">
                <th className="text-left px-6 py-3 font-medium">Project</th>
                <th className="text-left px-6 py-3 font-medium">Type</th>
                <th className="text-left px-6 py-3 font-medium">Tier</th>
                <th className="text-left px-6 py-3 font-medium">Status</th>
                <th className="text-left px-6 py-3 font-medium">Created</th>
              </tr>
            </thead>
            <tbody>
              {instances.slice(0, 8).map((inst) => (
                <tr
                  key={inst.projectId}
                  className="border-b border-border-primary last:border-0 hover:bg-surface-hover cursor-pointer transition-colors"
                  onClick={() => navigate(`/project/${inst.projectId}`)}
                >
                  <td className="px-6 py-3 font-medium text-text-primary">{inst.projectId}</td>
                  <td className="px-6 py-3 text-text-secondary">{dbIcon(inst.databaseType)} {inst.databaseType}</td>
                  <td className="px-6 py-3">
                    <span className="px-2 py-0.5 rounded text-xs bg-bg-tertiary text-text-secondary border border-border-primary">{inst.tier}</span>
                  </td>
                  <td className="px-6 py-3"><StatusBadge stage={inst.currentStage} /></td>
                  <td className="px-6 py-3 text-text-tertiary">{new Date(inst.createdAt).toLocaleDateString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </div>
  );
}

function dbIcon(type: string) {
  if (type === 'POSTGRESQL') return '🐘';
  if (type === 'MYSQL')      return '🐬';
  if (type === 'MONGODB')    return '🍃';
  return '🗄️';
}
