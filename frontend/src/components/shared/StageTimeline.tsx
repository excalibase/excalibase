import { ProvisioningStage } from '../../types';
import { cn } from '../../utils/cn';
import {
  CheckCircle, XCircle, Loader2,
  ShieldCheck, FolderPlus, Package, Clock, KeyRound, Archive, Activity,
} from 'lucide-react';

interface StageTimelineProps {
  readonly currentStage: ProvisioningStage;
  readonly failureReason?: string;
}

const STAGES = [
  { key: ProvisioningStage.VALIDATING,            label: 'Validating',  icon: ShieldCheck },
  { key: ProvisioningStage.NAMESPACE_CREATION,    label: 'Namespace',   icon: FolderPlus  },
  { key: ProvisioningStage.CRD_DEPLOYMENT,        label: 'Deploy CRD',  icon: Package     },
  { key: ProvisioningStage.WAITING_FOR_READY,     label: 'Waiting',     icon: Clock       },
  { key: ProvisioningStage.CREDENTIAL_GENERATION, label: 'Credentials', icon: KeyRound    },
  { key: ProvisioningStage.BACKUP_CONFIGURATION,  label: 'Backup',      icon: Archive     },
  { key: ProvisioningStage.METRICS_SETUP,         label: 'Metrics',     icon: Activity    },
  { key: ProvisioningStage.COMPLETED,             label: 'Completed',   icon: CheckCircle },
];

type StageState = 'done' | 'running' | 'failed' | 'pending';

function getLabelClass(state: StageState): string {
  if (state === 'running') return 'text-purple-400 font-semibold';
  if (state === 'done') return 'text-green-500';
  if (state === 'failed') return 'text-red-500';
  return 'text-text-tertiary';
}

export function StageTimeline({ currentStage, failureReason }: StageTimelineProps) {
  const currentIndex = STAGES.findIndex((s) => s.key === currentStage);
  const isFailed = currentStage === ProvisioningStage.FAILED;

  const getState = (index: number): StageState => {
    if (isFailed && index === currentIndex) return 'failed';
    if (currentStage === ProvisioningStage.COMPLETED || index < currentIndex) return 'done';
    if (index === currentIndex) return 'running';
    return 'pending';
  };

  return (
    <div className="space-y-4">
      <div className="flex items-center overflow-x-auto pb-2 gap-0">
        {STAGES.map((stage, index) => {
          const state = getState(index);
          const StageIcon = stage.icon;
          return (
            <div key={stage.key} className="flex items-center flex-shrink-0">
              <div className="flex flex-col items-center">
                <div
                  className={cn(
                    'w-10 h-10 rounded-full border-2 flex items-center justify-center',
                    state === 'done'    && 'border-green-500 bg-green-900/20',
                    state === 'running' && 'border-purple-500 bg-purple-900/10',
                    state === 'failed'  && 'border-red-500 bg-red-900/20',
                    state === 'pending' && 'border-border-primary bg-bg-tertiary'
                  )}
                >
                  {state === 'done'    && <CheckCircle className="w-5 h-5 text-green-500" />}
                  {state === 'running' && <Loader2 className="w-5 h-5 text-purple-400 animate-spin" />}
                  {state === 'failed'  && <XCircle className="w-5 h-5 text-red-500" />}
                  {state === 'pending' && <StageIcon className="w-4 h-4 text-text-tertiary" />}
                </div>
                <p className={cn('mt-2 text-xs text-center w-20', getLabelClass(state))}>
                  {stage.label}
                </p>
              </div>
              {index < STAGES.length - 1 && (
                <div className={cn(
                  'w-8 h-0.5 mb-6',
                  getState(index) === 'done' ? 'bg-green-500' : 'bg-border-secondary'
                )} />
              )}
            </div>
          );
        })}
      </div>

      {isFailed && failureReason && (
        <div className="bg-red-900/20 border border-color-error rounded-lg p-4 mt-2">
          <p className="text-color-error font-medium text-sm">Provisioning Failed</p>
          <p className="text-text-secondary text-xs mt-1">{failureReason}</p>
        </div>
      )}
    </div>
  );
}
