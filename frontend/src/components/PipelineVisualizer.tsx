import { ProvisioningStage } from '../types';
import { cn } from '../utils/cn';
import { STAGE_LABELS } from '../utils/stageLabels';
import { ProvisionFailure } from './shared/ProvisionFailure';
import { CheckCircle, Circle, XCircle, Loader2 } from 'lucide-react';

interface PipelineVisualizerProps {
  readonly currentStage: ProvisioningStage;
  readonly failureReason?: string;
  readonly failureKind?: string;
}

const STAGES = [
  { key: ProvisioningStage.VALIDATING },
  { key: ProvisioningStage.NAMESPACE_CREATION },
  { key: ProvisioningStage.CRD_DEPLOYMENT },
  { key: ProvisioningStage.WAITING_FOR_READY },
  { key: ProvisioningStage.CREDENTIAL_GENERATION },
  { key: ProvisioningStage.BACKUP_CONFIGURATION },
  { key: ProvisioningStage.METRICS_SETUP },
  { key: ProvisioningStage.COMPLETED },
];

export function PipelineVisualizer({ currentStage, failureReason, failureKind }: PipelineVisualizerProps) {
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
                {STAGE_LABELS[stage.key]}
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

      {isFailed && (
        <div className="bg-red-900/20 border border-color-error rounded-lg p-4">
          <p className="text-color-error font-medium">Provisioning failed</p>
          <ProvisionFailure reason={failureReason} kind={failureKind} className="text-text-secondary text-sm mt-1" />
        </div>
      )}
    </div>
  );
}
