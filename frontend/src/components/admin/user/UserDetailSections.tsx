import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import type { AuditRow, UserDetail } from '../../../api/adminConsole';
import { formatInstant } from '../../../utils/instant';
import { AccountStatusBadge } from '../AccountStatusBadge';
import { auditTarget } from '../v2/AuditLogTab';

const when = (iso?: string) => (iso ? formatInstant(iso) : '—');
const TRUST_LABEL: Record<string, string> = { limited: 'Limited', full: 'Full' };
const trustLabel = (state: string) => TRUST_LABEL[state] ?? state;

export function Card({ title, children }: { readonly title: string; readonly children: ReactNode }) {
  return (
    <section aria-label={title} className="bg-surface-card border border-border-primary rounded-xl p-4 min-w-0">
      <h2 className="mb-3 text-sm font-semibold text-text-primary">{title}</h2>
      {children}
    </section>
  );
}

function Fact({ label, children }: { readonly label: string; readonly children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-xs uppercase tracking-wide text-text-tertiary">{label}</dt>
      <dd className="mt-0.5 break-words text-sm text-text-primary">{children}</dd>
    </div>
  );
}

const Empty = ({ children }: { readonly children: ReactNode }) => <p className="text-sm text-text-tertiary">{children}</p>;

export function ProfileCard({ profile }: { readonly profile: UserDetail['profile'] }) {
  return (
    <Card title="Profile">
      <dl className="grid grid-cols-1 gap-3 sm:grid-cols-2">
        <Fact label="Username">{profile.username}</Fact>
        <Fact label="Role">{profile.role}</Fact>
        <Fact label="Email">
          <span>{profile.emailVerified ? 'Verified' : 'Not verified'}</span>
          {profile.emailVerifiedAt && <span className="text-text-tertiary">{` ${when(profile.emailVerifiedAt)}`}</span>}
          {profile.tempMail && <span className="ml-2 text-amber-400">temp mail</span>}
        </Fact>
        <Fact label="Signed up">{when(profile.signedUpAt)}</Fact>
        <Fact label="Last sign-in">{when(profile.lastLoginAt)}</Fact>
        <Fact label="Last sign-in address"><span className="font-mono text-xs">{profile.lastLoginIp || '—'}</span></Fact>
      </dl>
    </Card>
  );
}

export function TrustCard({ detail }: { readonly detail: UserDetail }) {
  const { trust } = detail.profile;
  return (
    <Card title="Trust">
      {trust ? (
        <div className="space-y-1 text-sm">
          <div><span className="font-medium">{trustLabel(trust.state)}</span> <span className="text-text-tertiary">via {trust.method}, {when(trust.decidedAt)}</span></div>
          <p className="text-text-secondary">{trust.reason}</p>
        </div>
      ) : <Empty>No trust decision recorded.</Empty>}
      {detail.trustHistory.length > 0 && (
        <ul aria-label="Trust history" className="mt-3 space-y-1 border-t border-border-primary pt-3 text-xs text-text-secondary">
          {detail.trustHistory.map((change) => (
            <li key={`${change.at}-${change.to}-${change.actor}`}>
              {change.from ? `${trustLabel(change.from)} to ${trustLabel(change.to)}` : `Started ${trustLabel(change.to)}`}
              {` by ${change.actor}: ${change.reason} `}
              <span className="text-text-tertiary">({when(change.at)})</span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

export function OrgsCard({ orgs }: { readonly orgs: UserDetail['orgs'] }) {
  return (
    <Card title="Organizations">
      {orgs.length === 0 ? <Empty>No organizations.</Empty> : (
        <ul className="divide-y divide-border-primary text-sm">
          {orgs.map((org) => (
            <li key={org.id} className="flex items-baseline justify-between gap-3 py-1.5">
              <span className="min-w-0 truncate font-medium" title={org.slug}>{org.name}</span>
              <span className="shrink-0 text-xs text-text-tertiary">{org.tier.toLowerCase()} · <span className="text-text-secondary">{org.role}</span></span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

export function ProjectsCard({ projects }: { readonly projects: UserDetail['projects'] }) {
  return (
    <Card title="Projects">
      {projects.length === 0 ? <Empty>No projects.</Empty> : (
        <ul className="divide-y divide-border-primary text-sm">
          {projects.map((project) => (
            <li key={project.projectId} className="flex items-baseline justify-between gap-3 py-1.5">
              <Link to={`/admin?tab=projects&project=${encodeURIComponent(project.projectId)}`} className="min-w-0 truncate font-medium underline" title={project.orgName}>{project.name || project.projectId}</Link>
              <span className="shrink-0 text-xs text-text-tertiary">{project.orgName} · {project.status.toLowerCase()}</span>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}

export function SignInCard({ methods }: { readonly methods: UserDetail['signIn'] }) {
  return (
    <Card title="Sign-in methods">
      <ul className="space-y-1 text-sm">
        {methods.map((method) => (
          <li key={`${method.kind}-${method.provider ?? ''}`}>
            {method.kind === 'password' ? 'Email and password' : <span className="capitalize">{method.provider}</span>}
            {method.email && <span className="text-text-tertiary">{` ${method.email}`}</span>}
            {method.linkedAt && <span className="text-text-tertiary">{`, linked ${when(method.linkedAt)}`}</span>}
          </li>
        ))}
      </ul>
    </Card>
  );
}

export function SuspensionCard({ detail, actions }: { readonly detail: UserDetail; readonly actions: ReactNode }) {
  const { suspension, suspensions } = detail;
  return (
    <Card title="Suspension">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="space-y-1 text-sm">
          <div className="flex items-center gap-2">
            <AccountStatusBadge status={detail.profile.status} />
            {!suspension.suspended && <span className="text-text-secondary">Not suspended</span>}
          </div>
          {suspension.suspended && (
            <p className="text-text-secondary">{suspension.reason || 'No reason recorded.'} <span className="text-text-tertiary">{suspension.reasonCode && `(${suspension.reasonCode}) `}{when(suspension.suspendedAt)}</span></p>
          )}
        </div>
        {actions}
      </div>
      <div className="mt-3 border-t border-border-primary pt-3">
        {suspensions.length === 0 ? <Empty>No suspensions on record.</Empty> : (
          <ul aria-label="Suspension history" className="space-y-1.5 text-sm">
            {suspensions.map((event) => (
              <li key={`${event.at}-${event.action}`}>
                <span className="font-medium">{event.action === 'user.suspend' ? 'Suspended by' : 'Unsuspended by'} {event.actorEmail || event.actorId || 'unknown'}</span>
                <span className="text-text-tertiary">{` ${when(event.at)}`}</span>
                {event.reason && <p className="text-text-secondary">{event.reason}</p>}
              </li>
            ))}
          </ul>
        )}
      </div>
    </Card>
  );
}

function AuditLine({ row, userId }: { readonly row: AuditRow; readonly userId: string }) {
  const target = row.resourceId === userId && row.resource === 'user' ? 'this account' : auditTarget(row).label;
  return (
    <li className="py-1.5 text-sm">
      <span className="font-mono text-xs">{row.action}</span>
      <span className="text-text-secondary">{` by ${row.actorEmail || row.actorId || 'system'} on ${target}`}</span>
      <span className="text-text-tertiary">{` ${when(row.at)}`}</span>
      {row.details && <p className="truncate text-xs text-text-tertiary" title={row.details}>{row.details}</p>}
    </li>
  );
}

export function RecentAuditCard({ rows, userId }: { readonly rows: AuditRow[]; readonly userId: string }) {
  return (
    <Card title="Recent audit">
      {rows.length === 0 ? <Empty>No audit entries yet.</Empty> : (
        <>
          <ul className="divide-y divide-border-primary">{rows.map((row) => <AuditLine key={row.id} row={row} userId={userId} />)}</ul>
          <Link to="/admin?tab=audit" className="mt-2 inline-block text-xs underline">Open the audit log</Link>
        </>
      )}
    </Card>
  );
}
