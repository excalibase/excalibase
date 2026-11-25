import { useEffect, useRef } from 'react';
import { X } from 'lucide-react';
import { ToneBadge } from '../../containers/ContainerBits';
import type { CardStatus } from './cardStatus';
import { PanelTabs } from './PanelTabs';
import type { PanelTab } from './panelState';

interface PanelFrameProps {
  readonly title: string;
  readonly subtitle?: string;
  readonly status?: CardStatus;
  readonly onClose: () => void;
  // Names the tab list for assistive technology: the kind of service, e.g. "Database".
  readonly tabsLabel?: string;
  readonly tabs?: readonly PanelTab[];
  readonly tab?: string;
  readonly onTab?: (key: string) => void;
  readonly idPrefix: string;
  readonly children: React.ReactNode;
}

// The side panel's header (name, status, close) and its tabs. A service with
// no tabs shows its one view under the header.
export function PanelFrame({ title, subtitle, status, onClose, tabsLabel = title, tabs = [], tab = '', onTab, idPrefix, children }: PanelFrameProps) {
  const panel = useRef<HTMLElement>(null);
  const close = useRef(onClose);
  close.current = onClose;
  // Opening a panel moves focus into it, and brings it into view when it is
  // stacked under the cards on a phone. Escape closes it when it came from
  // inside it: not from a dialog opened over it, which handles its own.
  useEffect(() => {
    const element = panel.current;
    if (!element) return undefined;
    element.focus({ preventScroll: true });
    element.scrollIntoView?.({ block: 'nearest' });
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !event.defaultPrevented) close.current();
    };
    element.addEventListener('keydown', onKeyDown);
    return () => element.removeEventListener('keydown', onKeyDown);
  }, []);
  return (
    <section ref={panel} tabIndex={-1} aria-label={`${title} panel`} className="min-w-0 rounded-xl outline-none border border-border-primary bg-surface-card p-5" data-testid="service-panel">
      <header className="mb-4 flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h2 className="truncate text-lg font-semibold text-text-primary">{title}</h2>
          {subtitle && <p className="truncate text-xs text-text-tertiary">{subtitle}</p>}
        </div>
        <div className="flex items-center gap-2">
          {status && <ToneBadge {...status} testId="panel-status" />}
          <button type="button" aria-label="Close panel" onClick={onClose} className="rounded-lg p-1.5 text-text-tertiary hover:bg-surface-hover hover:text-text-primary">
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
      </header>
      {tabs.length > 0 && onTab ? (
        <PanelTabs label={tabsLabel} tabs={tabs} active={tab} onSelect={onTab} idPrefix={idPrefix}>
          {children}
        </PanelTabs>
      ) : (
        children
      )}
    </section>
  );
}
