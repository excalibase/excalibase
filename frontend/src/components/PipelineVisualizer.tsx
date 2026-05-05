import { ProvisioningStage } from '../types';
import { cn } from '../utils/cn';
import { CheckCircle, Circle, XCircle, Loader2 } from 'lucide-react';

interface PipelineVisualizerProps {
  readonly currentStage: ProvisioningStage;
  readonly failureReason?: string;
}

const STAGES = [
  { key: ProvisioningStage.VALIDATING, label: 'Validating' },
  { key: ProvisioningStage.NAMESPACE_CREATION, label: 'Creating Namespace' },
  { key: ProvisioningStage.CRD_DEPLOYMENT, label: 'Deploying CRD' },
  { key: ProvisioningStage.WAITING_FOR_READY, label: 'Waiting for Ready' },
  { key: ProvisioningStage.CREDENTIAL_GENERATION, label: 'Generating Credentials' },
  { key: ProvisioningStage.BACKUP_CONFIGURATION, label: 'Configuring Backup' },
  { key: ProvisioningStage.METRICS_SETUP, label: 'Setting up Metrics' },
  { key: ProvisioningStage.COMPLETED, label: 'Completed' },
];

export function PipelineVisualizer({ currentStage, failureReason }: PipelineVisualizerProps) {
  const currentIndex = STAGES.findIndex((s) => s.key === currentStage);
  const isFailed = currentStage === ProvisioningStage.FAILED;

  const getStageIcon = (index: number) => {
    if (isFailed && index === currentIndex) {
      return <XCircle className="w-6 h-6 text-color-error" />;
    }
    if (index < currentIndex || currentStage === ProvisioningStage.COMPLETED) {
      return <CheckCircle className="w-6 h-6 text-color-success" />;
    }
    if (index === currentIndex) {
      return <Loader2 className="w-6 h-6 text-accent-primary animate-spin" />;
    }
    return <Circle className="w-6 h-6 text-text-tertiary" />;
  };

  const getNodeClass = (index: number) => {
    if (index < currentIndex || currentStage === ProvisioningStage.COMPLETED) {
      return 'border-color-success bg-green-900/20';
    }
    if (index === currentIndex && !isFailed) {
      return 'border-accent-primary bg-blue-900/20';
    }
    if (isFailed && index === currentIndex) {
      return 'border-color-error bg-red-900/20';
    }
    return 'border-border-primary bg-bg-tertiary';
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between overflow-x-auto pb-4">
        {STAGES.map((stage, index) => (
          <div key={stage.key} className="flex items-center flex-shrink-0">
            <div className="flex flex-col items-center">
              <div
                className={cn(
                  'flex items-center justify-center w-12 h-12 rounded-full border-2',
                  getNodeClass(index)
                )}
              >
                {getStageIcon(index)}
              </div>
              <p
                className={cn(
                  'mt-2 text-sm text-center w-32',
                  index === currentIndex
                    ? 'text-text-primary font-medium'
                    : 'text-text-tertiary'
                )}
              >
                {stage.label}
              </p>
            </div>
            {index < STAGES.length - 1 && (
              <div
                className={cn(
                  'w-16 h-0.5 mx-2 mb-8',
                  index < currentIndex
                    ? 'bg-color-success'
                    : 'bg-border-secondary'
                )}
              />
            )}
          </div>
        ))}
      </div>

      {isFailed && failureReason && (
        <div className="bg-red-900/20 border border-color-error rounded-lg p-4">
          <p className="text-color-error font-medium">Provisioning Failed</p>
          <p className="text-text-secondary text-sm mt-1">{failureReason}</p>
        </div>
      )}
    </div>
  );
}
