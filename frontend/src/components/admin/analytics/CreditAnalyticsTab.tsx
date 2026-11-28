import { useCreditAnalytics } from '../../../hooks/useAdminAnalytics';
import { KpiRow, Lede, Loaded } from './AnalyticsKit';

// Who has run out of credit and what that paused. Current state, not a window: the numbers are read when the tab opens.
export function CreditAnalyticsTab() {
  const query = useCreditAnalytics(true);
  return (
    <div className="space-y-4">
      <Lede title="Credit">Free orgs by whether their credit is left, and the services paused because it ran out.</Lede>
      <Loaded query={query}>
        {(data) => (
          <KpiRow items={[
            { label: 'Orgs at $0', value: data.orgsAtZero, note: 'credit used up or expired', tone: 'bad' },
            { label: 'Orgs with credit', value: data.orgsWithCredit, note: 'free orgs with a balance' },
            { label: 'Orgs with paused services', value: data.orgsWithPausedServices, note: 'past the grace period' },
            { label: 'Services paused', value: data.servicesPaused, note: 'databases and apps, data kept' },
          ]}
          />
        )}
      </Loaded>
    </div>
  );
}
