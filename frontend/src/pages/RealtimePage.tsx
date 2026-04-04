import { Radio } from 'lucide-react';

export function RealtimePage() {
  return (
    <div data-testid="realtime-page">
      <h3 className="text-lg font-semibold text-text-primary mb-1">Realtime</h3>
      <p className="text-sm text-text-secondary mb-6">Live CDC (Change Data Capture) streaming</p>

      <div className="max-w-2xl space-y-6">
        {/* Architecture info */}
        <div className="rounded-lg border border-border-primary bg-surface-card p-6">
          <div className="flex items-center gap-3 mb-4">
            <div className="p-3 rounded-full bg-purple-500/10">
              <Radio className="w-6 h-6 text-purple-400" />
            </div>
            <div>
              <h4 className="text-sm font-medium text-text-primary">CDC via excalibase-watcher</h4>
              <p className="text-xs text-text-tertiary">PostgreSQL WAL-based change data capture</p>
            </div>
          </div>

          <div className="space-y-3 text-sm">
            <div className="flex items-center justify-between py-2 border-b border-border-primary">
              <span className="text-text-secondary">Capture Method</span>
              <span className="text-xs text-text-primary">PostgreSQL Logical Replication (WAL)</span>
            </div>
            <div className="flex items-center justify-between py-2 border-b border-border-primary">
              <span className="text-text-secondary">Transport</span>
              <span className="text-xs text-text-primary">NATS JetStream</span>
            </div>
            <div className="flex items-center justify-between py-2 border-b border-border-primary">
              <span className="text-text-secondary">NATS Subject Pattern</span>
              <code className="text-xs text-text-primary font-mono bg-bg-primary px-2 py-1 rounded">cdc.{'{schema}'}.{'{table}'}</code>
            </div>
            <div className="flex items-center justify-between py-2">
              <span className="text-text-secondary">Client Access</span>
              <span className="text-xs text-text-primary">Via excalibase-rest / excalibase-graphql subscriptions</span>
            </div>
          </div>
        </div>

        {/* Events captured */}
        <div className="rounded-lg border border-border-primary bg-surface-card p-6">
          <h4 className="text-sm font-medium text-text-primary mb-3">Events Captured</h4>
          <div className="flex flex-wrap gap-2">
            {['INSERT', 'UPDATE', 'DELETE', 'TRUNCATE', 'DDL'].map(op => (
              <span key={op} className={`px-2.5 py-1 rounded text-xs font-medium ${
                op === 'INSERT' ? 'bg-green-500/10 text-green-400 border border-green-500/30' :
                op === 'UPDATE' ? 'bg-blue-500/10 text-blue-400 border border-blue-500/30' :
                op === 'DELETE' ? 'bg-red-500/10 text-red-400 border border-red-500/30' :
                'bg-bg-tertiary text-text-secondary border border-border-primary'
              }`}>{op}</span>
            ))}
          </div>
        </div>

        {/* Coming soon */}
        <div className="rounded-lg border border-dashed border-border-secondary bg-bg-secondary p-6 text-center">
          <Radio className="w-8 h-8 text-text-tertiary mx-auto mb-2" />
          <p className="text-sm text-text-secondary">Live Inspector coming soon</p>
          <p className="text-xs text-text-tertiary mt-1">Real-time event viewer with table filtering and pause/resume</p>
        </div>
      </div>
    </div>
  );
}
