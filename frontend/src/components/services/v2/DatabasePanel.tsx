import { DatabaseRequired } from '../../DatabaseRequired';
import { DatabaseRunning } from '../../DatabaseRunning';
import { lazy, Suspense } from 'react';
import { Spinner } from '../../containers/ContainerBits';
import type { DatabaseInstance } from '../../../types';
import { databaseServiceStatus } from '../serviceStatus';
import { DbBackupsTab } from './DbBackupsTab';
import { DbConnectTab } from './DbConnectTab';
import { DbLogsTab } from './DbLogsTab';
import { DbMetricsTab } from './DbMetricsTab';
import { DbOverviewTab } from './DbOverviewTab';
import { DbSettingsTab } from './DbSettingsTab';
import { PanelFrame } from './PanelFrame';
import { tabsFor } from './panelState';
import { engineLabel } from '../../../utils/engine';

// The table browser still embeds its v1 page (EXC-857 split it out) and loads
// only when its tab opens.
const TablesPage = lazy(() => import('../../../pages/TablesPage').then((m) => ({ default: m.TablesPage })));

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
          <DbBackupsTab project={project} />
        </DatabaseRequired>
      );
    case 'metrics':
      return (
        <DatabaseRequired mysql>
          <DbMetricsTab project={project} />
        </DatabaseRequired>
      );
    case 'logs':
      return (
        <DatabaseRequired mysql>
          <DbLogsTab project={project} />
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
