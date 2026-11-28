import { useEdgeFunctions, useRuntimeStatus } from '../../../hooks/useEdgeFunctions';
import { useProjectRole } from '../../../hooks/useProjectRole';
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
  const { canDevelop, isLoading: roleLoading } = useProjectRole(projectId);
  if (error || !functions || roleLoading) {
    return (
      <PanelFrame title="Functions" onClose={onClose} idPrefix="functions">
        {error ? <p role="alert" className="text-sm text-red-400">{serverErrorMessage(error, 'Could not load the functions')}</p> : <Spinner />}
      </PanelFrame>
    );
  }
  // Logs and secrets need the developer role on the server, so a viewer is not offered them.
  const tabs = tabsFor('functions').filter((t) => canDevelop || t.key === 'functions');
  const active = tabs.some((t) => t.key === tab) ? tab : 'functions';
  return (
    <PanelFrame
      title="Functions"
      subtitle={functionsCardDetail(functions.length)}
      status={functionsCardStatus(runtime)}
      tabsLabel="Functions"
      tabs={tabs}
      tab={active}
      onTab={onTab}
      onClose={onClose}
      idPrefix="functions"
    >
      {active === 'logs' && <FunctionLogsTab projectId={projectId} functions={functions} />}
      {active === 'secrets' && <FunctionSecretsTab projectId={projectId} />}
      {active === 'functions' && <FunctionsTab projectId={projectId} functions={functions} />}
    </PanelFrame>
  );
}
