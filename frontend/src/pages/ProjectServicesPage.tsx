import { useCallback, useMemo } from 'react';
import { useNavigate, useParams, useSearchParams, type To } from 'react-router-dom';
import { apiErrorMessage } from '../api/apps';
import { AddServiceMenu, EmptyServices } from '../components/services/v2/AddService';
import { ServicePanel } from '../components/services/v2/ServicePanel';
import { ServiceList, ServicesLoading } from '../components/services/v2/ServiceList';
import { panelSearch, parsePanelState, type ServiceRef } from '../components/services/v2/panelState';
import { hasService, isEmptyProject, useProjectServices } from '../components/services/v2/useProjectServices';

// The project's Services tab (studio_v2): a card per service on the left, the
// selected service's panel beside it, stacked under the cards on a phone. The
// selection is in the URL, so it can be linked to and the back button works.
export function ProjectServicesPage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const services = useProjectServices(projectId);
  const { service, tab } = useMemo(() => parsePanelState(params), [params]);

  const to = useCallback(
    (next: ServiceRef | null, nextTab?: string): To => ({ search: panelSearch(params, next, nextTab).toString() }),
    [params],
  );
  const open = service && !services.loading && hasService(services, service) ? service : null;
  const gone = service !== null && !services.loading && open === null;

  if (services.error && !services.project) {
    return <p role="alert" className="py-16 text-center text-sm text-red-400">{apiErrorMessage(services.error, 'Could not load the project')}</p>;
  }
  if (services.loading && !services.project) return <ServicesLoading />;
  if (isEmptyProject(services)) return <EmptyServices projectId={projectId} services={services} />;

  return (
    <div className="mx-auto max-w-7xl space-y-4" data-testid="project-services">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-xl font-bold text-text-primary">{services.project?.projectName || projectId}</h2>
          <p className="text-sm text-text-tertiary">The services in this project. Each one runs and reports on its own.</p>
        </div>
        <AddServiceMenu projectId={projectId} services={services} />
      </div>
      {services.error != null && <p role="alert" className="text-sm text-red-400">{apiErrorMessage(services.error, 'Some services could not be loaded')}</p>}
      {gone && <p role="status" className="rounded-lg border border-border-primary bg-surface-card px-4 py-3 text-sm text-text-secondary">That service is not in this project, so there is nothing to open.</p>}
      <div className="grid gap-6 lg:grid-cols-[minmax(16rem,22rem)_minmax(0,1fr)]">
        <ServiceList projectId={projectId} services={services} selected={open} to={(next) => to(next)} />
        {open && (
          <ServicePanel
            projectId={projectId}
            service={open}
            tab={tab}
            database={services.database}
            onTab={(key) => navigate(to(open, key))}
            onClose={() => navigate(to(null))}
          />
        )}
      </div>
    </div>
  );
}
