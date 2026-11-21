import { lazy, Suspense } from 'react';
import { Spinner } from '../../containers/ContainerBits';
import type { DatabaseInstance } from '../../../types';
import { AppPanel } from './AppPanel';
import { DatabasePanel } from './DatabasePanel';
import { PanelFrame } from './PanelFrame';
import type { ServiceRef } from './panelState';
import { SitePanel } from './SitePanel';

const StoragePage = lazy(() => import('../../../pages/StoragePage').then((m) => ({ default: m.StoragePage })));

interface ServicePanelProps {
  readonly projectId: string;
  readonly service: ServiceRef;
  readonly tab: string;
  // The database, when the project has one; the panel for a database is only built from it.
  readonly database?: DatabaseInstance;
  readonly onTab: (key: string) => void;
  readonly onClose: () => void;
}

// The side panel of the selected service. Each tab shows the page or component
// the service already has; this only arranges them.
export function ServicePanel({ projectId, service, tab, database, onTab, onClose }: ServicePanelProps) {
  switch (service.kind) {
    case 'database':
      return database ? <DatabasePanel project={database} tab={tab} onTab={onTab} onClose={onClose} /> : null;
    case 'app':
      return <AppPanel projectId={projectId} appId={service.id ?? ''} tab={tab} onTab={onTab} onClose={onClose} />;
    case 'site':
      return <SitePanel projectId={projectId} siteId={service.id ?? ''} tab={tab} onTab={onTab} onClose={onClose} />;
    case 'storage':
      return (
        <PanelFrame title={service.id ?? 'Storage'} subtitle="Storage bucket" onClose={onClose} idPrefix="storage">
          <Suspense fallback={<Spinner />}>
            <StoragePage initialBucket={service.id} />
          </Suspense>
        </PanelFrame>
      );
    case 'functions':
      return null;
  }
}
