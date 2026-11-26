import { useFeatureEnabled } from '../../../hooks/useDeploymentMode';
import { useTrafficAnalytics } from '../../../hooks/useAdminAnalytics';
import { BarChart } from './BarChart';
import { KpiRow, Lede, Loaded, Notice, Panel, shortDay } from './AnalyticsKit';

export function TrafficAnalyticsTab() {
  const query = useTrafficAnalytics(true);
  const { enabled: requestMetricsOn } = useFeatureEnabled('requestmetrics');
  return (
    <div className="space-y-4">
      <Lede title="Traffic">Requests, bandwidth and database load.</Lede>
      <Notice title="Request and network numbers are not collected yet.">
        <p>
          Request counts need <code>FEATURE_REQUESTMETRICS</code>{requestMetricsOn ? ' (on here, but they are not charted in this view yet)' : ' (off on this install)'}.
          Container and host network samples exist only where single-host monitoring or the net meter runs. Database queries per second is not recorded.
        </p>
      </Notice>
      <Loaded query={query}>
        {(data) => (
          <>
            <KpiRow items={[
              { label: 'API requests', value: null, note: 'not collected yet' },
              { label: 'Egress', value: null, note: 'not collected yet' },
              { label: 'DB queries/s', value: null, note: 'not recorded' },
              { label: 'Peak DB connections', value: data.peakConnections, note: `all projects, last ${data.windowDays} days` },
            ]}
            />
            <Panel title={`Peak database connections per day, last ${data.windowDays} days`} hint="The one traffic signal recorded today: the sum of each project's daily peak.">
              <BarChart
                label="Peak database connections" labels={data.peakConnectionsPerDay.map((day) => shortDay(day.day))}
                series={[data.peakConnectionsPerDay.map((day) => day.total)]} seriesClasses={['fill-accent-primary']}
                tip={(i) => `${data.peakConnectionsPerDay[i].total} connections`}
              />
            </Panel>
          </>
        )}
      </Loaded>
    </div>
  );
}
