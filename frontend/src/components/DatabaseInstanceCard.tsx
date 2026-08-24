import { useState } from 'react';
import type { DatabaseInstance } from '../types';
import { Card } from './Card';
import { Button } from './Button';
import { PipelineVisualizer } from './PipelineVisualizer';
import { CredentialsViewer } from './CredentialsViewer';
import { MonitoringDashboard } from './MonitoringDashboard';
import { ChevronDown, ChevronUp, Database, Trash2, Info, Activity, Key } from 'lucide-react';
import { useDeprovisionDatabase } from '../hooks/useProvisioning';

interface DatabaseInstanceCardProps {
  readonly instance: DatabaseInstance;
}

type TabType = 'details' | 'monitoring' | 'credentials';

export function DatabaseInstanceCard({ instance }: DatabaseInstanceCardProps) {
  const [expanded, setExpanded] = useState(false);
  const [activeTab, setActiveTab] = useState<TabType>('details');
  const deprovision = useDeprovisionDatabase();

  const handleDelete = async () => {
    if (confirm(`Are you sure you want to delete ${instance.projectId}?`)) {
      await deprovision.mutateAsync(instance.projectId);
    }
  };

  const getStatusBadge = () => {
    if (instance.currentStage === 'FAILED') {
      return (
        <span className="px-3 py-1 bg-red-900/20 border border-color-error text-color-error text-sm rounded-full">
          Failed
        </span>
      );
    }
    if (instance.currentStage === 'COMPLETED') {
      return (
        <span className="px-3 py-1 bg-green-900/20 border border-color-success text-color-success text-sm rounded-full">
          Active
        </span>
      );
    }
    return (
      <span className="px-3 py-1 bg-blue-900/20 border border-blue-500 text-blue-400 text-sm rounded-full">
        Provisioning
      </span>
    );
  };

  return (
    <Card>
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-4 flex-1">
          <Database className="w-10 h-10 text-emerald-400" />
          <div>
            <h3 className="text-lg font-semibold text-text-primary">
              {instance.projectId}
            </h3>
            <p className="text-sm text-text-secondary">
              {instance.databaseType} · {instance.tier} · {instance.namespace}
            </p>
          </div>
        </div>
        <div className="flex items-center gap-3">
          {getStatusBadge()}
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setExpanded(!expanded)}
          >
            {expanded ? (
              <ChevronUp className="w-5 h-5" />
            ) : (
              <ChevronDown className="w-5 h-5" />
            )}
          </Button>
        </div>
      </div>

      {expanded && (
        <div className="mt-6 space-y-6">
          {/* Tab Navigation */}
          <div className="flex gap-2 border-b border-border-primary">
            <TabButton
              icon={<Info className="w-4 h-4" />}
              label="Details"
              active={activeTab === 'details'}
              onClick={() => setActiveTab('details')}
            />
            <TabButton
              icon={<Activity className="w-4 h-4" />}
              label="Monitoring"
              active={activeTab === 'monitoring'}
              onClick={() => setActiveTab('monitoring')}
            />
            <TabButton
              icon={<Key className="w-4 h-4" />}
              label="Credentials"
              active={activeTab === 'credentials'}
              onClick={() => setActiveTab('credentials')}
            />
            <div className="ml-auto flex items-end pb-2">
              <Button
                variant="danger"
                size="sm"
                onClick={handleDelete}
                disabled={deprovision.isPending}
              >
                <Trash2 className="w-4 h-4 mr-2" />
                Delete
              </Button>
            </div>
          </div>

          {/* Tab Content */}
          <div className="pt-2">
            {activeTab === 'details' && (
              <div className="space-y-6">
                {/* Pipeline Visualizer */}
                <div>
                  <h4 className="text-sm font-medium text-text-primary mb-4">
                    Provisioning Pipeline
                  </h4>
                  <PipelineVisualizer
                    currentStage={instance.currentStage}
                    failureReason={instance.failureReason}
                  />
                </div>

                {/* Instance Details */}
                <div className="grid grid-cols-2 gap-4">
                  <div>
                    <p className="text-sm text-text-tertiary">Organization</p>
                    <p className="text-text-primary font-medium">{instance.orgId}</p>
                  </div>
                  <div>
                    <p className="text-sm text-text-tertiary">Database Name</p>
                    <p className="text-text-primary font-medium">{instance.databaseName}</p>
                  </div>
                  <div>
                    <p className="text-sm text-text-tertiary">Host</p>
                    <p className="text-text-primary font-medium">{instance.host}</p>
                  </div>
                  <div>
                    <p className="text-sm text-text-tertiary">Port</p>
                    <p className="text-text-primary font-medium">{instance.port}</p>
                  </div>
                  <div>
                    <p className="text-sm text-text-tertiary">Created</p>
                    <p className="text-text-primary font-medium">
                      {new Date(instance.createdAt).toLocaleString()}
                    </p>
                  </div>
                  <div>
                    <p className="text-sm text-text-tertiary">Backup Enabled</p>
                    <p className="text-text-primary font-medium">
                      {instance.backupEnabled ? 'Yes' : 'No'}
                    </p>
                  </div>
                </div>
              </div>
            )}

            {activeTab === 'monitoring' && (
              <MonitoringDashboard projectId={instance.projectId} />
            )}

            {activeTab === 'credentials' && (
              <CredentialsViewer projectId={instance.projectId} />
            )}
          </div>
        </div>
      )}
    </Card>
  );
}

interface TabButtonProps {
  readonly icon: React.ReactNode;
  readonly label: string;
  readonly active: boolean;
  readonly onClick: () => void;
}

function TabButton({ icon, label, active, onClick }: TabButtonProps) {
  return (
    <button
      onClick={onClick}
      className={`flex items-center gap-2 px-4 py-2 border-b-2 transition-colors ${
        active
          ? 'border-emerald-500 text-emerald-400'
          : 'border-transparent text-text-secondary hover:text-text-primary'
      }`}
    >
      {icon}
      <span className="text-sm font-medium">{label}</span>
    </button>
  );
}
