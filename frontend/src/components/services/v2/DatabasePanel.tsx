import { DatabaseRequired } from '../../DatabaseRequired';
import { DatabaseRunning } from '../../DatabaseRunning';
import { lazy, Suspense } from 'react';
import { Spinner } from '../../containers/ContainerBits';
import type { DatabaseInstance } from '../../../types';
import { databaseServiceStatus } from '../serviceStatus';
import { DbConnectTab } from './DbConnectTab';
import { DbOverviewTab } from './DbOverviewTab';
import { DbSettingsTab } from './DbSettingsTab';
import { PanelFrame } from './PanelFrame';
import { tabsFor } from './panelState';
import { engineLabel } from '../../../utils/engine';

// The heavy pages load only when their tab opens.
const TablesPage = lazy(() => import('../../../pages/TablesPage').then((m) => ({ default: m.TablesPage })));
const BackupsPage = lazy(() => import('../../../pages/BackupsPage').then((m) => ({ default: m.BackupsPage })));
const MetricsPage = lazy(() => import('../../../pages/MetricsPage').then((m) => ({ default: m.MetricsPage })));
const LogExplorerPage = lazy(() => import('../../../pages/LogExplorerPage').then((m) => ({ default: m.LogExplorerPage })));

interface PanelProps {
  readonly tab: string;
  readonly onTab: (key: string) => void;
  readonly onClose: () => void;
}

// The guards are the ones the pages have on their own routes: a MySQL project
// or one still being set up gets the same explanation here.
function TabBody({ project, tab }: { readonly project: DatabaseInstance; readonly tab: string }) {
  switch (tab) {
    case 'connect':
      return <DbConnectTab project={project} />;
    case 'data':
      return (
        <DatabaseRequired mysql>
          <DatabaseRunning>
            <TablesPage />
          </DatabaseRunning>
        </DatabaseRequired>
      );
    case 'backups':
      return (
        <DatabaseRequired mysql>
          <BackupsPage />
        </DatabaseRequired>
      );
    case 'metrics':
      return (
        <DatabaseRequired mysql>
          <MetricsPage />
        </DatabaseRequired>
      );
    case 'logs':
      return (
        <DatabaseRequired mysql>
          <LogExplorerPage />
        </DatabaseRequired>
      );
    case 'settings':
      return <DbSettingsTab project={project} />;
    default:
      return <DbOverviewTab project={project} />;
  }
}

export function DatabasePanel({ project, tab, onTab, onClose }: PanelProps & { readonly project: DatabaseInstance }) {
  return (
    <PanelFrame
      title="Database"
      subtitle={engineLabel(project)}
      status={databaseServiceStatus(project)}
      tabsLabel="Database"
      tabs={tabsFor('database')}
      tab={tab}
      onTab={onTab}
      onClose={onClose}
      idPrefix="db"
    >
      <Suspense fallback={<Spinner />}>
        <TabBody project={project} tab={tab} />
      </Suspense>
    </PanelFrame>
  );
}
