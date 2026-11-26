import { KpiRow, Lede, Notice } from './AnalyticsKit';

// Nothing is read here: storage is not measured yet, and a tab of zeros would
// read as "empty disks". The platform tab keeps the storage budget that does exist.
export function StorageAnalyticsTab() {
  return (
    <div className="space-y-4">
      <Lede title="Storage">Disk used by databases, files and backups.</Lede>
      <KpiRow items={[
        { label: 'Database size', value: null, note: 'not collected yet' },
        { label: 'Backups', value: null, note: 'not collected yet' },
        { label: 'File storage', value: null, note: 'not collected yet' },
        { label: 'Volume pool', value: null, note: 'see the Platform tab' },
      ]}
      />
      <Notice title="Database sizes are not collected yet.">
        <p>The size each database reports is always zero today, so no figure is shown rather than a false one.</p>
      </Notice>
      <Notice title="Backups are not listed here yet.">
        <p>The platform keeps no backup records for this view, so backups may be tracked only in the cluster.</p>
      </Notice>
    </div>
  );
}
