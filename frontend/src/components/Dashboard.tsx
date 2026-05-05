import { useState } from 'react';
import { useInstances, useInstanceSSE } from '../hooks/useProvisioning';
import { Card, CardHeader, CardTitle, CardContent } from './Card';
import { Button } from './Button';
import { DatabaseInstanceCard } from './DatabaseInstanceCard';
import { ProvisioningForm } from './ProvisioningForm';
import { Database, Plus, Loader2 } from 'lucide-react';

export function Dashboard() {
  const { data: instances, isLoading, error } = useInstances();
  const [showProvisionForm, setShowProvisionForm] = useState(false);

  // Subscribe to SSE for all provisioning instances
  const provisioningInstances = instances?.filter(
    (i) => i.currentStage !== 'COMPLETED' && i.currentStage !== 'FAILED'
  ) || [];

  // Render SSE manager for provisioning instances
  const sseManagers = provisioningInstances.map((instance) => (
    <ProvisioningSSEManager key={instance.projectId} projectId={instance.projectId} />
  ));

  if (isLoading) {
    return (
      <div className="flex items-center justify-center min-h-screen">
        <Loader2 className="w-8 h-8 animate-spin text-accent-primary" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex items-center justify-center min-h-screen">
        <Card className="max-w-md">
          <CardContent>
            <p className="text-color-error">Failed to load instances: {error instanceof Error ? error.message : String(error)}</p>
          </CardContent>
        </Card>
      </div>
    );
  }

  return (
    <>
      {/* Hidden SSE managers for real-time updates */}
      {sseManagers}

      <div className="min-h-screen p-8">
        <div className="max-w-7xl mx-auto">
          {/* Header */}
          <div className="flex items-center justify-between mb-8">
            <div className="flex items-center gap-3">
              <Database className="w-8 h-8 text-accent-primary" />
              <h1 className="text-3xl font-bold text-text-primary">
                Excalibase Provisioning
              </h1>
            </div>
            <Button onClick={() => setShowProvisionForm(true)} className="flex items-center gap-2">
              <Plus className="w-5 h-5" />
              Provision Database
            </Button>
          </div>

        {/* Stats */}
        <div className="grid grid-cols-1 md:grid-cols-3 gap-6 mb-8">
          <Card>
            <CardHeader>
              <CardTitle>Total Instances</CardTitle>
            </CardHeader>
            <CardContent>
              <p className="text-3xl font-bold text-text-primary">
                {instances?.length ?? 0}
              </p>
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>Active</CardTitle>
            </CardHeader>
            <CardContent>
              <p className="text-3xl font-bold text-color-success">
                {instances?.filter((i) => i.status === 'ACTIVE').length ?? 0}
              </p>
            </CardContent>
          </Card>
          <Card>
            <CardHeader>
              <CardTitle>Failed</CardTitle>
            </CardHeader>
            <CardContent>
              <p className="text-3xl font-bold text-color-error">
                {instances?.filter((i) => i.currentStage === 'FAILED').length ?? 0}
              </p>
            </CardContent>
          </Card>
        </div>

        {/* Instances List */}
        <div className="space-y-4">
          {instances && instances.length > 0 ? (
            instances.map((instance) => (
              <DatabaseInstanceCard key={instance.id} instance={instance} />
            ))
          ) : (
            <Card>
              <CardContent className="text-center py-12">
                <Database className="w-16 h-16 mx-auto mb-4 text-text-tertiary" />
                <p className="text-text-secondary mb-4">No database instances found</p>
                <Button onClick={() => setShowProvisionForm(true)}>
                  Create your first database
                </Button>
              </CardContent>
            </Card>
          )}
        </div>
      </div>

        {/* Provisioning Form Modal */}
        {showProvisionForm && (
          <ProvisioningForm onClose={() => setShowProvisionForm(false)} />
        )}
      </div>
    </>
  );
}

// Helper component to manage SSE connections. Subscribes for the side-
// effect (live state push) — the parent reads provisioning state through
// React Query separately. No render output by design.
function ProvisioningSSEManager({ projectId }: { projectId: string }) {
  useInstanceSSE(projectId);
  return null;
}
