import { useCurrentMetrics, useMetricsSSE, useMetricsHistory } from '../hooks/useMetrics';
import { Card, CardHeader, CardTitle, CardContent } from './Card';
import {
  AreaChart,
  Area,
  XAxis,
  YAxis,
  CartesianGrid,
  Tooltip,
  ResponsiveContainer,
  Legend
} from 'recharts';
import { Activity, Database, HardDrive, Cpu, Clock, Zap } from 'lucide-react';

interface MonitoringDashboardProps {
  projectId: string;
}

export function MonitoringDashboard({ projectId }: MonitoringDashboardProps) {
  const { data: currentMetrics, isLoading } = useCurrentMetrics(projectId);
  const { data: historyData } = useMetricsHistory(projectId, 20);
  const { latestMetrics, isConnected } = useMetricsSSE(projectId, true);

  // Use SSE data if available, otherwise use current metrics
  const metrics = latestMetrics || currentMetrics;

  if (isLoading || !metrics) {
    return (
      <div className="text-center py-8">
        <Activity className="w-8 h-8 animate-pulse text-accent-primary mx-auto mb-2" />
        <p className="text-text-secondary">Loading metrics...</p>
      </div>
    );
  }

  const getHealthStatusColor = (status: string) => {
    switch (status) {
      case 'HEALTHY':
        return 'text-color-success';
      case 'DEGRADED':
        return 'text-yellow-500';
      case 'DOWN':
        return 'text-color-error';
      default:
        return 'text-text-secondary';
    }
  };

  const formatTimestamp = (timestamp: string) => {
    return new Date(timestamp).toLocaleTimeString();
  };

  // Prepare chart data
  const chartData = historyData?.metrics.map((m) => ({
    time: formatTimestamp(m.timestamp),
    cpu: Math.round(m.cpuUsagePercent),
    memory: Math.round(m.memoryUsagePercent),
    disk: Math.round(m.diskUsagePercent),
  })) || [];

  return (
    <div className="space-y-6">
      {/* Connection Status */}
      <div className="flex items-center justify-between">
        <h3 className="text-lg font-semibold text-text-primary">Monitoring Dashboard</h3>
        <div className="flex items-center gap-2">
          <div className={`w-2 h-2 rounded-full ${isConnected ? 'bg-green-500 animate-pulse' : 'bg-gray-500'}`} />
          <span className="text-sm text-text-secondary">
            {isConnected ? 'Live' : 'Offline'}
          </span>
        </div>
      </div>

      {/* Health Status */}
      <Card>
        <CardContent className="pt-6">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-3">
              <Activity className="w-8 h-8 text-accent-primary" />
              <div>
                <p className="text-sm text-text-tertiary">Health Status</p>
                <p className={`text-2xl font-bold ${getHealthStatusColor(metrics.healthStatus)}`}>
                  {metrics.healthStatus}
                </p>
              </div>
            </div>
            <div className="text-right">
              <p className="text-sm text-text-tertiary">Last Updated</p>
              <p className="text-sm text-text-primary">{formatTimestamp(metrics.timestamp)}</p>
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Resource Usage Cards */}
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <MetricCard
          icon={<Cpu className="w-6 h-6" />}
          label="CPU Usage"
          value={`${Math.round(metrics.cpuUsagePercent)}%`}
          subtitle={`${metrics.cpuUsageCores?.toFixed(2)} cores`}
          color="text-blue-500"
        />
        <MetricCard
          icon={<Database className="w-6 h-6" />}
          label="Memory Usage"
          value={`${Math.round(metrics.memoryUsagePercent)}%`}
          subtitle={`${metrics.memoryUsageMB} MB`}
          color="text-purple-500"
        />
        <MetricCard
          icon={<HardDrive className="w-6 h-6" />}
          label="Disk Usage"
          value={`${Math.round(metrics.diskUsagePercent)}%`}
          subtitle={`${metrics.diskUsageGB} GB`}
          color="text-orange-500"
        />
      </div>

      {/* Resource Usage Chart */}
      {chartData.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle>Resource Usage Trends</CardTitle>
          </CardHeader>
          <CardContent>
            <ResponsiveContainer width="100%" height={250}>
              <AreaChart data={chartData}>
                <defs>
                  <linearGradient id="colorCpu" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#3b82f6" stopOpacity={0.8} />
                    <stop offset="95%" stopColor="#3b82f6" stopOpacity={0} />
                  </linearGradient>
                  <linearGradient id="colorMemory" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#a855f7" stopOpacity={0.8} />
                    <stop offset="95%" stopColor="#a855f7" stopOpacity={0} />
                  </linearGradient>
                  <linearGradient id="colorDisk" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="5%" stopColor="#f97316" stopOpacity={0.8} />
                    <stop offset="95%" stopColor="#f97316" stopOpacity={0} />
                  </linearGradient>
                </defs>
                <CartesianGrid strokeDasharray="3 3" stroke="#374151" />
                <XAxis dataKey="time" stroke="#9ca3af" style={{ fontSize: '12px' }} />
                <YAxis stroke="#9ca3af" style={{ fontSize: '12px' }} />
                <Tooltip
                  contentStyle={{ backgroundColor: '#1f2937', border: '1px solid #374151' }}
                  labelStyle={{ color: '#f3f4f6' }}
                />
                <Legend />
                <Area type="monotone" dataKey="cpu" stroke="#3b82f6" fillOpacity={1} fill="url(#colorCpu)" name="CPU %" />
                <Area type="monotone" dataKey="memory" stroke="#a855f7" fillOpacity={1} fill="url(#colorMemory)" name="Memory %" />
                <Area type="monotone" dataKey="disk" stroke="#f97316" fillOpacity={1} fill="url(#colorDisk)" name="Disk %" />
              </AreaChart>
            </ResponsiveContainer>
          </CardContent>
        </Card>
      )}

      {/* Database Metrics */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <Card>
          <CardHeader>
            <CardTitle>Connections</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-3">
              <MetricRow label="Active" value={metrics.activeConnections} />
              <MetricRow label="Idle" value={metrics.idleConnections} />
              <MetricRow label="Max" value={metrics.maxConnections} />
              <div className="pt-2 border-t border-border-primary">
                <div className="flex justify-between items-center">
                  <span className="text-sm text-text-tertiary">Usage</span>
                  <span className="text-sm font-medium text-text-primary">
                    {Math.round((metrics.activeConnections / metrics.maxConnections) * 100)}%
                  </span>
                </div>
                <div className="mt-2 h-2 bg-bg-tertiary rounded-full overflow-hidden">
                  <div
                    className="h-full bg-accent-primary rounded-full"
                    style={{ width: `${(metrics.activeConnections / metrics.maxConnections) * 100}%` }}
                  />
                </div>
              </div>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Performance</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-3">
              <MetricRow
                icon={<Zap className="w-4 h-4" />}
                label="Queries/sec"
                value={metrics.queriesPerSecond.toFixed(1)}
              />
              <MetricRow
                icon={<Clock className="w-4 h-4" />}
                label="Avg Latency"
                value={`${metrics.averageQueryLatencyMs.toFixed(1)}ms`}
              />
              <MetricRow label="Slow Queries" value={metrics.slowQueryCount} />
              <MetricRow label="DB Size" value={`${metrics.databaseSizeGB} GB`} />
            </div>
          </CardContent>
        </Card>
      </div>

      {/* Backup Status */}
      {(metrics.lastBackupTime || metrics.nextBackupTime) && (
        <Card>
          <CardHeader>
            <CardTitle>Backup Status</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="grid grid-cols-2 gap-4">
              {metrics.lastBackupTime && (
                <div>
                  <p className="text-sm text-text-tertiary">Last Backup</p>
                  <p className="text-sm font-medium text-text-primary">
                    {new Date(metrics.lastBackupTime).toLocaleString()}
                  </p>
                </div>
              )}
              {metrics.nextBackupTime && (
                <div>
                  <p className="text-sm text-text-tertiary">Next Backup</p>
                  <p className="text-sm font-medium text-text-primary">
                    {new Date(metrics.nextBackupTime).toLocaleString()}
                  </p>
                </div>
              )}
            </div>
          </CardContent>
        </Card>
      )}
    </div>
  );
}

interface MetricCardProps {
  icon: React.ReactNode;
  label: string;
  value: string;
  subtitle: string;
  color: string;
}

function MetricCard({ icon, label, value, subtitle, color }: MetricCardProps) {
  return (
    <Card>
      <CardContent className="pt-6">
        <div className="flex items-center gap-3">
          <div className={color}>{icon}</div>
          <div>
            <p className="text-sm text-text-tertiary">{label}</p>
            <p className="text-2xl font-bold text-text-primary">{value}</p>
            <p className="text-xs text-text-secondary">{subtitle}</p>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

interface MetricRowProps {
  label: string;
  value: string | number;
  icon?: React.ReactNode;
}

function MetricRow({ label, value, icon }: MetricRowProps) {
  return (
    <div className="flex justify-between items-center">
      <div className="flex items-center gap-2">
        {icon && <span className="text-text-tertiary">{icon}</span>}
        <span className="text-sm text-text-tertiary">{label}</span>
      </div>
      <span className="text-sm font-medium text-text-primary">{value}</span>
    </div>
  );
}
