import { ProvisioningStage } from '../types';

// The product word for each provisioning stage. Customers see these, never the
// raw stage names (EXC-850).
export const STAGE_LABELS: Readonly<Record<string, string>> = {
  [ProvisioningStage.VALIDATING]: 'Validating',
  [ProvisioningStage.NAMESPACE_CREATION]: 'Environment',
  [ProvisioningStage.CRD_DEPLOYMENT]: 'Database',
  [ProvisioningStage.WAITING_FOR_READY]: 'Waiting',
  [ProvisioningStage.CREDENTIAL_GENERATION]: 'Credentials',
  [ProvisioningStage.BACKUP_CONFIGURATION]: 'Backup',
  [ProvisioningStage.METRICS_SETUP]: 'Metrics',
  [ProvisioningStage.WATCHER_DEPLOYMENT]: 'Change feed',
  [ProvisioningStage.ROLE_CREATION]: 'Roles',
  [ProvisioningStage.COMPLETED]: 'Completed',
};

export function stageLabel(stage: string | undefined): string {
  return (stage && STAGE_LABELS[stage]) || 'an unknown step';
}
