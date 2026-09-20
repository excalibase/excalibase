import { useState } from 'react';
import { useProvisionDatabase } from '../hooks/useProvisioning';
import { DatabaseType, TierType } from '../types';
import { Button } from './Button';
import { Card, CardTitle } from './Card';
import { X } from 'lucide-react';
import { usePostgresCatalog, findMajor } from '../api/postgresCatalog';
import { PostgresVersionPicker } from './PostgresVersionPicker';

interface ProvisioningFormProps {
  readonly onClose: () => void;
}

export function ProvisioningForm({ onClose }: ProvisioningFormProps) {
  const [formData, setFormData] = useState({
    projectName: '',
    orgId: '',
    databaseType: DatabaseType.POSTGRESQL,
    tier: TierType.FREE,
    // No default major: the API refuses a request that names none, and
    // choosing one here would be choosing it for the customer.
    postgresVersion: '',
    documentDb: false,
  });

  const provision = useProvisionDatabase();
  const catalog = usePostgresCatalog();

  const chooseVersion = (postgresVersion: string) => {
    const entry = findMajor(catalog.data, postgresVersion);
    setFormData((previous) => ({
      ...previous,
      postgresVersion,
      documentDb: entry?.documentDb ? previous.documentDb : false,
    }));
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await provision.mutateAsync(formData);
      onClose();
    } catch {
      // Error is shown via React Query's error state
    }
  };

  return (
    <div className="fixed inset-0 bg-black/50 flex items-center justify-center p-4 z-50">
      <Card className="w-full max-w-2xl">
        <div className="flex items-center justify-between mb-6">
          <CardTitle>Provision New Database</CardTitle>
          <button
            onClick={onClose}
            className="text-text-tertiary hover:text-text-primary transition-colors"
          >
            <X className="w-6 h-6" />
          </button>
        </div>

        <form onSubmit={handleSubmit} className="space-y-4">
          <div>
            <label htmlFor="provision-project-name" className="block text-sm font-medium text-text-primary mb-2">
              Project Name
            </label>
            <input
              id="provision-project-name"
              type="text"
              value={formData.projectName}
              onChange={(e) => setFormData({ ...formData, projectName: e.target.value })}
              className="w-full px-4 py-2 bg-bg-tertiary border border-border-primary rounded-lg text-text-primary focus:outline-none focus:ring-2 focus:ring-accent-primary"
              placeholder="my-project"
              required
            />
            <p className="text-xs text-text-tertiary mt-1">
              Lowercase alphanumeric and hyphens only
            </p>
          </div>

          <div>
            <label htmlFor="provision-org-id" className="block text-sm font-medium text-text-primary mb-2">
              Organization ID
            </label>
            <input
              id="provision-org-id"
              type="text"
              value={formData.orgId}
              onChange={(e) => setFormData({ ...formData, orgId: e.target.value })}
              className="w-full px-4 py-2 bg-bg-tertiary border border-border-primary rounded-lg text-text-primary focus:outline-none focus:ring-2 focus:ring-accent-primary"
              placeholder="my-org"
              required
            />
          </div>

          <div>
            <label htmlFor="provision-db-type" className="block text-sm font-medium text-text-primary mb-2">
              Database Type
            </label>
            <select
              id="provision-db-type"
              value={formData.databaseType}
              onChange={(e) =>
                setFormData({ ...formData, databaseType: e.target.value as typeof formData.databaseType })
              }
              className="w-full px-4 py-2 bg-bg-tertiary border border-border-primary rounded-lg text-text-primary focus:outline-none focus:ring-2 focus:ring-accent-primary"
            >
              <option value={DatabaseType.POSTGRESQL}>PostgreSQL</option>
              <option value={DatabaseType.MYSQL} disabled>MySQL (coming soon)</option>
              <option value={DatabaseType.MONGODB} disabled>MongoDB (coming soon)</option>
            </select>
          </div>

          <PostgresVersionPicker
            catalog={catalog.data}
            isLoading={catalog.isLoading}
            error={catalog.error}
            version={formData.postgresVersion}
            onVersionChange={chooseVersion}
            documentDb={formData.documentDb}
            onDocumentDbChange={(documentDb) => setFormData((previous) => ({ ...previous, documentDb }))}
          />

          <div>
            <p className="block text-sm font-medium text-text-primary mb-2">
              Tier
            </p>
            <div className="grid grid-cols-3 gap-3">
              {([TierType.FREE, TierType.STANDARD, TierType.ENTERPRISE] as const).map((tier) => (
                <button
                  key={tier}
                  type="button"
                  onClick={() => setFormData({ ...formData, tier: tier as typeof formData.tier })}
                  className={`p-4 rounded-lg border-2 transition-colors ${
                    formData.tier === tier
                      ? 'border-accent-primary bg-blue-900/20'
                      : 'border-border-primary bg-bg-tertiary hover:border-border-secondary'
                  }`}
                >
                  <p className="font-semibold text-text-primary">{tier}</p>
                  <p className="text-xs text-text-tertiary mt-1">
                    {tier === TierType.FREE && '1 instance, 5GB'}
                    {tier === TierType.STANDARD && '3 replicas, 50GB'}
                    {tier === TierType.ENTERPRISE && '5 replicas, 500GB'}
                  </p>
                </button>
              ))}
            </div>
          </div>

          <div className="flex gap-3 pt-4">
            <Button type="button" variant="secondary" onClick={onClose} className="flex-1">
              Cancel
            </Button>
            <Button
              type="submit"
              disabled={provision.isPending || !formData.postgresVersion}
              className="flex-1"
            >
              {provision.isPending ? 'Provisioning...' : 'Provision Database'}
            </Button>
          </div>

          {provision.isError && (
            <div className="bg-red-900/20 border border-color-error rounded-lg p-3">
              <p className="text-color-error text-sm">
                {provision.error instanceof Error ? provision.error.message : String(provision.error)}
              </p>
            </div>
          )}
        </form>
      </Card>
    </div>
  );
}
