import { useEdgeFunctions, useRuntimeStatus } from '../../../hooks/useEdgeFunctions';
import { serverErrorMessage } from '../../../utils/serverError';
import { Spinner } from '../../containers/ContainerBits';
import { functionsCardDetail, functionsCardStatus } from './cardStatus';
import { FunctionLogsTab } from './FunctionLogsTab';
import { FunctionSecretsTab } from './FunctionSecretsTab';
import { FunctionsTab } from './FunctionsTab';
import { PanelFrame } from './PanelFrame';
import { tabsFor } from './panelState';

interface FunctionsPanelProps {
  readonly projectId: string;
  readonly tab: string;
  readonly onTab: (key: string) => void;
  readonly onClose: () => void;
}

// The project's edge functions as one service: they share a runtime, a set of
// secrets and an outbound-host list, so the panel is one too.
export function FunctionsPanel({ projectId, tab, onTab, onClose }: FunctionsPanelProps) {
  const { data: functions, error } = useEdgeFunctions(projectId);
  const { data: runtime } = useRuntimeStatus(projectId);
  if (error || !functions) {
    return (
      <PanelFrame title="Functions" onClose={onClose} idPrefix="functions">
        {error ? <p role="alert" className="text-sm text-red-400">{serverErrorMessage(error, 'Could not load the functions')}</p> : <Spinner />}
      </PanelFrame>
    );
  }
  return (
    <PanelFrame
      title="Functions"
      subtitle={functionsCardDetail(functions.length)}
      status={functionsCardStatus(runtime)}
      tabsLabel="Functions"
      tabs={tabsFor('functions')}
      tab={tab}
      onTab={onTab}
      onClose={onClose}
      idPrefix="functions"
    >
      {tab === 'logs' && <FunctionLogsTab projectId={projectId} functions={functions} />}
      {tab === 'secrets' && <FunctionSecretsTab projectId={projectId} />}
      {tab !== 'logs' && tab !== 'secrets' && <FunctionsTab projectId={projectId} functions={functions} />}
    </PanelFrame>
  );
}
