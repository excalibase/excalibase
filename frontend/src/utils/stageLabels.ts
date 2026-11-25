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

// Undefined for a stage with no product word, so a caller never prints a raw key.
export function stageLabel(stage: string | undefined): string | undefined {
  return stage && Object.hasOwn(STAGE_LABELS, stage) ? STAGE_LABELS[stage] : undefined;
}

export function failureHeadline(stage: string | undefined): string {
  const label = stageLabel(stage);
  return label ? `Provisioning failed at stage ${label}` : 'Provisioning failed';
}
