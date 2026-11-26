import { cn } from '../../../utils/cn';
import { useAbuseAnalytics } from '../../../hooks/useAdminAnalytics';
import { useFeatureEnabled } from '../../../hooks/useDeploymentMode';
import { ANALYTICS_TABS } from './analyticsTabs';

interface Chip {
  id: string;
  tone: 'bad' | 'warn';
  text: string;
  tab: string;
}

// The few things worth a look first, each a jump to the tab that explains it.
export function AttentionChips({ onGo }: { readonly onGo: (tab: string) => void }) {
  const abuse = useAbuseAnalytics(true);
  const { enabled: requestMetricsOn } = useFeatureEnabled('requestmetrics');
  const chips: Chip[] = [];
  const stillActive = abuse.data?.refusedStillActive ?? 0;
  if (stillActive > 0) chips.push({ id: 'abuse', tone: 'bad', text: `${stillActive} accounts had a deploy refused and are still active`, tab: ANALYTICS_TABS.abuse });
  if (!requestMetricsOn) chips.push({ id: 'traffic', tone: 'warn', text: 'Request metrics are off', tab: ANALYTICS_TABS.traffic });
  chips.push({ id: 'storage', tone: 'warn', text: 'Storage numbers are not collected yet', tab: ANALYTICS_TABS.storage });
  return (
    <div aria-label="Needs attention" className="flex flex-wrap gap-2">
      {chips.map((chip) => (
        <button
          key={chip.id} type="button" onClick={() => onGo(chip.tab)}
          className="flex items-center gap-2 rounded-md border border-border-primary bg-surface-card px-3 py-1.5 text-sm text-text-primary hover:border-text-tertiary"
        >
          <i className={cn('w-2 h-2 rounded-full', chip.tone === 'bad' ? 'bg-color-error' : 'bg-color-warning')} />
          {chip.text}
        </button>
      ))}
    </div>
  );
}
