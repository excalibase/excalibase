import { useCurrentMetrics, useMetricsHistory } from '../../../hooks/useMetrics';
import { useFeatureEnabled } from '../../../hooks/useDeploymentMode';
import { databaseSize } from '../../../utils/databaseSize';
import type { DatabaseInstance, DatabaseMetrics, MetricsHistory, PodMetrics } from '../../../types';
import { MetricsEndpointCard } from '../../MetricsEndpointCard';
import { ProjectContainersSection } from '../../monitoring/ProjectContainersSection';
import { formatWhen } from '../../containers/appCopy';
import { Spinner, ToneBadge } from '../../containers/ContainerBits';
import { StatusBadge } from '../../shared/StatusBadge';
import { MetricsTrend, type TrendPoint } from './MetricsTrend';
import { Fact, Notice, Section } from './sections';
import { parseInstant } from '../../../utils/instant';

const NOT_AVAILABLE = 'Not available';
const HISTORY_POINTS = 30;

function plural(count: number | null | undefined): string {
  if (count == null) return '';
  return ` (${count} ${count === 1 ? 'instance' : 'instances'})`;
}

function cpuText(m: DatabaseMetrics): { value: string; limit?: string } {
  const value = m.cpuUsageCores == null ? NOT_AVAILABLE : `${m.cpuUsageCores.toFixed(2)} cores`;
  return { value, limit: m.cpuLimitCores == null ? undefined : `/ ${m.cpuLimitCores} limit${plural(m.instanceCount)}` };
}

function memoryText(m: DatabaseMetrics): { value: string; limit?: string } {
  const value = m.memoryUsageMB == null ? NOT_AVAILABLE : `${m.memoryUsageMB} MB`;
  return { value, limit: m.memoryLimitMB == null ? undefined : `/ ${m.memoryLimitMB} MB limit` };
}

function Reading({ label, value, limit }: { readonly label: string; readonly value: string; readonly limit?: string }) {
  return (
    <Fact label={label}>
      <span className="text-base font-semibold">{value}</span>
      {limit && <span className="block text-xs text-text-secondary">{limit}</span>}
    </Fact>
  );
}

function Readings({ metrics }: { readonly metrics: DatabaseMetrics }) {
  const cpu = cpuText(metrics);
  const memory = memoryText(metrics);
  const size = databaseSize(metrics);
  const connections = metrics.activeConnections == null ? NOT_AVAILABLE : `${metrics.activeConnections} of ${metrics.maxConnections ?? '—'}`;
  return (
    <dl className="grid grid-cols-2 gap-4 sm:grid-cols-3">
      <Reading label="CPU" {...cpu} />
      <Reading label="Memory" {...memory} />
      <Reading label="Storage" value={metrics.storageLimit ?? NOT_AVAILABLE} limit={size && `${size} used`} />
      <Reading label="Connections" value={connections} />
      <Fact label="Health">{metrics.healthStatus ? <StatusBadge status={metrics.healthStatus} /> : NOT_AVAILABLE}</Fact>
      <Reading label="Last backup" value={metrics.lastBackupTime ? formatWhen(metrics.lastBackupTime) : 'None'} />
    </dl>
  );
}

function Bar({ value, max, color }: { readonly value: number; readonly max: number; readonly color: string }) {
  const percent = max > 0 ? Math.min((value / max) * 100, 100) : 0;
  return (
    <div className="mt-1 h-1.5 w-full rounded-full bg-bg-tertiary">
      <div className={`h-1.5 rounded-full ${color}`} style={{ width: `${percent}%` }} />
    </div>
  );
}

function InstanceRow({ instance }: { readonly instance: PodMetrics }) {
  return (
    <li data-testid={`instance-${instance.name}`} className="space-y-2 py-3">
      <div className="flex items-center justify-between gap-2">
        <span className="break-all font-mono text-xs text-text-primary">{instance.name}</span>
        <ToneBadge label={instance.role} tone={instance.role === 'primary' ? 'progress' : 'neutral'} />
      </div>
      <div className="grid grid-cols-2 gap-3 text-xs text-text-secondary">
        <div>
          CPU {instance.cpuCores.toFixed(3)} / {instance.cpuLimitCores}
          <Bar value={instance.cpuCores} max={instance.cpuLimitCores} color="bg-blue-500" />
        </div>
        <div>
          Memory {instance.memoryMB} / {instance.memoryLimitMB} MB
          <Bar value={instance.memoryMB} max={instance.memoryLimitMB} color="bg-purple-500" />
        </div>
      </div>
    </li>
  );
}

function trendOf(history: MetricsHistory | undefined): TrendPoint[] {
  const round = (value: number | null) => (value == null ? null : Math.round(value));
  return (history?.metrics ?? [])
    .filter((m) => m.metricsAvailable)
    .map((m) => ({ time: new Date(parseInstant(m.timestamp)).toLocaleTimeString(), cpu: round(m.cpuUsagePercent), memory: round(m.memoryUsagePercent) }));
}

const hasUsage = (points: readonly TrendPoint[]) => points.length > 1 && points.some((p) => (p.cpu ?? 0) > 0 || (p.memory ?? 0) > 0);

function Trend({ projectId }: { readonly projectId: string }) {
  const { data } = useMetricsHistory(projectId, HISTORY_POINTS);
  const points = trendOf(data);
  if (!hasUsage(points)) return null;
  return (
    <Section id="db-trend" title="Usage over time">
      <MetricsTrend data={points} />
    </Section>
  );
}

function Unavailable({ reason }: { readonly reason: string | null }) {
  return (
    <Section id="db-metrics-unavailable" title="Metrics unavailable">
      <p className="text-sm text-text-secondary">{reason ?? 'Usage cannot be read from the database right now.'}</p>
    </Section>
  );
}

function Body({ projectId, metrics }: { readonly projectId: string; readonly metrics: DatabaseMetrics }) {
  if (!metrics.metricsAvailable) return <Unavailable reason={metrics.unavailableReason} />;
  const instances = metrics.pods ?? [];
  return (
    <>
      <Section id="db-usage" title="Usage now">
        <Readings metrics={metrics} />
      </Section>
      {instances.length > 0 && (
        <Section id="db-instances" title="Instances">
          <ul className="divide-y divide-border-primary">{instances.map((i) => <InstanceRow key={i.name} instance={i} />)}</ul>
        </Section>
      )}
      <Trend projectId={projectId} />
    </>
  );
}

// Live use against the plan's limits, then the history, host containers and the
// Prometheus endpoint where those features are on.
export function DbMetricsTab({ project }: { readonly project: DatabaseInstance }) {
  const { data: metrics, isLoading } = useCurrentMetrics(project.projectId);
  const { enabled: hostMetrics } = useFeatureEnabled('hostmetrics');
  return (
    <div className="space-y-5" data-testid="db-metrics">
      {isLoading && <Spinner />}
      {!isLoading && metrics == null && <Notice>No metrics yet.</Notice>}
      {metrics != null && <Body projectId={project.projectId} metrics={metrics} />}
      {hostMetrics && <ProjectContainersSection projectId={project.projectId} />}
      <MetricsEndpointCard projectId={project.projectId} />
    </div>
  );
}
