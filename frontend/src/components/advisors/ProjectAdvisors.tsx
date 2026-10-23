import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { AlertTriangle, Loader2, RefreshCw, Shield } from 'lucide-react';
import { useQueryClient } from '@tanstack/react-query';
import { useProjectAdvisors } from '../../hooks/useSchemaAdvisors';
import { apiErrorMessage } from '../../api/apps';
import type { AdvisorCategory, ProjectAdvisorCheck, ProjectAdvisorFinding } from '../../types/schema';

const CHECK_TITLES: Record<string, string> = {
  fk_without_index: 'Foreign key without an index',
  unused_index: 'Unused index',
  duplicate_index: 'Duplicate index',
  table_without_primary_key: 'Table without a primary key',
  large_table_without_index_use: 'Large table read by full scans',
  anon_unfiltered_access: 'Open to anyone without a row filter',
  write_without_select: 'Write permission without select',
};

const LEVEL_COLORS: Record<string, string> = {
  error: 'bg-red-900/20 text-red-400 border-red-500/30',
  warning: 'bg-yellow-900/20 text-yellow-400 border-yellow-500/30',
  info: 'bg-blue-900/20 text-blue-400 border-blue-500/30',
};

const checkTitle = (id: string) => CHECK_TITLES[id] ?? id;

function subject(finding: ProjectAdvisorFinding): string {
  const parts = [finding.object];
  if (finding.index) parts.push(`index ${finding.index}`);
  if (finding.role) parts.push([finding.role, finding.operation].filter(Boolean).join(' '));
  return parts.join(' · ');
}

function FindingCard({ finding }: { readonly finding: ProjectAdvisorFinding }) {
  return (
    <div className="rounded-lg border border-border-primary bg-surface-card p-4" data-testid={`advisor-finding-${finding.check}`}>
      <div className="flex items-center gap-2 mb-2">
        <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs border ${LEVEL_COLORS[finding.level] ?? LEVEL_COLORS.info}`}>
          {finding.level}
        </span>
        <h4 className="text-sm font-medium text-text-primary flex-1">{checkTitle(finding.check)}</h4>
        <span className="text-xs text-text-tertiary font-mono">{subject(finding)}</span>
      </div>
      <p className="text-sm text-text-secondary mb-2">{finding.message}</p>
      <p className="text-sm text-text-primary">{finding.fixHint}</p>
      {finding.fixSql && (
        <pre
          className="mt-2 p-3 rounded bg-bg-primary text-xs text-text-secondary font-mono overflow-x-auto border border-border-primary"
          data-testid="advisor-fix-sql"
        >
          {finding.fixSql}
        </pre>
      )}
    </div>
  );
}

function SkippedChecks({ checks }: { readonly checks: readonly ProjectAdvisorCheck[] }) {
  const skipped = checks.filter((check) => check.status !== 'ran');
  if (skipped.length === 0) return null;
  return (
    <ul className="mt-6 space-y-1 text-xs text-text-tertiary">
      {skipped.map((check) => (
        <li key={check.id} data-testid={`advisor-check-${check.id}`}>
          <span className="text-text-secondary">{checkTitle(check.id)}</span>
          {check.status === 'not_applicable' ? ' does not apply here: ' : ' was not run: '}
          {check.reason}
        </li>
      ))}
    </ul>
  );
}

const TABS: readonly { key: AdvisorCategory; label: string; icon: typeof Shield }[] = [
  { key: 'security', label: 'Security', icon: Shield },
  { key: 'performance', label: 'Performance', icon: AlertTriangle },
];

// The project's security and performance checks on Postgres or MySQL (EXC-655).
export function ProjectAdvisors() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const queryClient = useQueryClient();
  const [tab, setTab] = useState<AdvisorCategory>('security');
  const { data, isLoading, isFetching, error } = useProjectAdvisors(projectId, true);
  const findings = (data?.findings ?? []).filter((finding) => finding.category === tab);
  const checks = (data?.checks ?? []).filter((check) => check.category === tab);
  const count = (category: AdvisorCategory) => (data?.findings ?? []).filter((finding) => finding.category === category).length;

  return (
    <div data-testid="advisors-page">
      <div className="flex items-center justify-between mb-2">
        <h3 className="text-lg font-semibold text-text-primary">Advisors</h3>
        <button
          onClick={() => queryClient.invalidateQueries({ queryKey: ['project-advisors', projectId] })}
          disabled={isFetching}
          className="flex items-center gap-2 px-3 py-2 bg-purple-500 hover:bg-purple-600 disabled:opacity-60 text-white text-sm font-medium rounded-lg transition-colors"
          data-testid="rescan-btn"
        >
          <RefreshCw className={`w-4 h-4 ${isFetching ? 'animate-spin' : ''}`} /> Re-run
        </button>
      </div>
      <p className="text-xs text-text-tertiary mb-4">
        Read-only checks of this database and its API permissions. Suggested SQL is never run for you: review it, then run it in the SQL editor.
      </p>

      <div className="flex gap-1 mb-4 border-b border-border-primary">
        {TABS.map(({ key, label, icon: Icon }) => (
          <button
            key={key}
            onClick={() => setTab(key)}
            data-testid={`tab-${key}`}
            className={`flex items-center gap-2 px-4 py-2 text-sm font-medium border-b-2 transition-colors ${
              tab === key ? 'border-purple-500 text-purple-400' : 'border-transparent text-text-tertiary hover:text-text-primary'
            }`}
          >
            <Icon className="w-4 h-4" /> {label}
            {count(key) > 0 && <span className="px-1.5 py-0.5 rounded text-xs bg-orange-900/20 text-orange-400">{count(key)}</span>}
          </button>
        ))}
      </div>

      {isLoading && (
        <div className="flex justify-center py-16"><Loader2 className="w-8 h-8 animate-spin text-purple-400" /></div>
      )}
      {error && (
        <p role="alert" className="py-8 text-center text-sm text-red-400">{apiErrorMessage(error, 'Could not run the advisors')}</p>
      )}
      {data && findings.length === 0 && (
        <div className="rounded-lg border border-border-primary bg-surface-card p-12 text-center text-text-tertiary text-sm">
          No {tab} findings.
        </div>
      )}
      {findings.length > 0 && (
        <div className="space-y-3">
          {findings.map((finding) => (
            <FindingCard key={[finding.check, subject(finding), finding.constraint, finding.columns?.join(',')].join(':')} finding={finding} />
          ))}
        </div>
      )}
      <SkippedChecks checks={checks} />
    </div>
  );
}
