import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { Loader2, RefreshCw, AlertTriangle, Shield } from 'lucide-react';
import { usePerformanceAdvisor, useSecurityAdvisor } from '../hooks/useSchema';
import { useQueryClient } from '@tanstack/react-query';
import type { AdvisorFinding } from '../types/schema';

const SEVERITY_COLORS: Record<string, string> = {
  critical: 'bg-red-900/20 text-red-400 border-red-500/30',
  high: 'bg-orange-900/20 text-orange-400 border-orange-500/30',
  medium: 'bg-yellow-900/20 text-yellow-400 border-yellow-500/30',
  low: 'bg-blue-900/20 text-blue-400 border-blue-500/30',
};

function SeverityBadge({ severity }: { readonly severity: string }) {
  const color = SEVERITY_COLORS[severity] || SEVERITY_COLORS.low;
  return (
    <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs border ${color}`}>
      {severity}
    </span>
  );
}

function FindingCard({ finding }: { readonly finding: AdvisorFinding }) {
  return (
    <div className="rounded-lg border border-border-primary bg-surface-card p-4">
      <div className="flex items-center gap-2 mb-2">
        <SeverityBadge severity={finding.severity} />
        <h4 className="text-sm font-medium text-text-primary flex-1">{finding.title}</h4>
        <span className="text-xs text-text-tertiary font-mono">{finding.ruleId}</span>
      </div>
      <p className="text-sm text-text-secondary mb-2">{finding.description}</p>
      {finding.table && (
        <p className="text-xs text-text-tertiary mb-2">
          Affected table: <span className="font-mono text-text-secondary">{finding.table}</span>
        </p>
      )}
      {finding.fix && (
        <pre className="mt-2 p-3 rounded bg-bg-primary text-xs text-text-secondary font-mono overflow-x-auto border border-border-primary">
          {finding.fix}
        </pre>
      )}
    </div>
  );
}

type Tab = 'performance' | 'security';

export function AdvisorsPage() {
  const { projectId } = useParams<{ projectId: string }>();
  const queryClient = useQueryClient();
  const [activeTab, setActiveTab] = useState<Tab>('performance');

  const { data: perfFindings = [], isLoading: perfLoading } = usePerformanceAdvisor(projectId || '');
  const { data: secFindings = [], isLoading: secLoading } = useSecurityAdvisor(projectId || '');

  const findings = activeTab === 'performance' ? perfFindings : secFindings;
  const isLoading = activeTab === 'performance' ? perfLoading : secLoading;

  const rescan = () => {
    queryClient.invalidateQueries({ queryKey: ['advisor-performance', projectId] });
    queryClient.invalidateQueries({ queryKey: ['advisor-security', projectId] });
  };

  return (
    <div data-testid="advisors-page">
      <div className="flex items-center justify-between mb-4">
        <h3 className="text-lg font-semibold text-text-primary">Database Advisors</h3>
        <button
          onClick={rescan}
          className="flex items-center gap-2 px-3 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg transition-colors"
          data-testid="rescan-btn"
        >
          <RefreshCw className="w-4 h-4" /> Re-scan
        </button>
      </div>

      {/* Tabs */}
      <div className="flex gap-1 mb-4 border-b border-border-primary">
        <button
          onClick={() => setActiveTab('performance')}
          data-testid="tab-performance"
          className={`flex items-center gap-2 px-4 py-2 text-sm font-medium border-b-2 transition-colors ${
            activeTab === 'performance'
              ? 'border-purple-500 text-purple-400'
              : 'border-transparent text-text-tertiary hover:text-text-primary'
          }`}
        >
          <AlertTriangle className="w-4 h-4" /> Performance
          {perfFindings.length > 0 && (
            <span className="px-1.5 py-0.5 rounded text-xs bg-orange-900/20 text-orange-400">{perfFindings.length}</span>
          )}
        </button>
        <button
          onClick={() => setActiveTab('security')}
          data-testid="tab-security"
          className={`flex items-center gap-2 px-4 py-2 text-sm font-medium border-b-2 transition-colors ${
            activeTab === 'security'
              ? 'border-purple-500 text-purple-400'
              : 'border-transparent text-text-tertiary hover:text-text-primary'
          }`}
        >
          <Shield className="w-4 h-4" /> Security
          {secFindings.length > 0 && (
            <span className="px-1.5 py-0.5 rounded text-xs bg-yellow-900/20 text-yellow-400">{secFindings.length}</span>
          )}
        </button>
      </div>

      {/* Content */}
      {isLoading && (
        <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>
      )}
      {!isLoading && findings.length === 0 && (
        <div className="rounded-lg border border-border-primary bg-surface-card p-12 text-center text-text-tertiary text-sm">
          No {activeTab} findings. Your database looks good!
        </div>
      )}
      {!isLoading && findings.length > 0 && (
        <div className="space-y-3">
          {findings.map((f) => <FindingCard key={f.ruleId} finding={f} />)}
        </div>
      )}
    </div>
  );
}
