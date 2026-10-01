import { Check, Filter, Minus } from 'lucide-react';
import { OPERATIONS, type Operation, type PermissionDocument } from '../../api/permissions';
import { cellStatus, permissionFor, type CellStatus } from '../../utils/permissionModel';

interface PermissionGridProps {
  readonly doc: PermissionDocument | undefined;
  readonly table: string;
  readonly roles: string[];
  readonly disabled?: boolean;
  readonly onOpen: (role: string, operation: Operation) => void;
}

const STATUS: Record<CellStatus, { label: string; className: string; Icon: typeof Check }> = {
  none: { label: 'No access', className: 'text-text-tertiary border-border-primary', Icon: Minus },
  full: { label: 'Full access', className: 'text-emerald-400 border-emerald-500/30 bg-emerald-500/5', Icon: Check },
  custom: { label: 'Custom', className: 'text-amber-400 border-amber-500/30 bg-amber-500/5', Icon: Filter },
};

/** Roles down, operations across; each cell opens that permission's editor. */
export function PermissionGrid({ doc, table, roles, disabled, onOpen }: PermissionGridProps) {
  return (
    <div className="rounded-lg border border-border-primary overflow-x-auto">
      <table className="w-full text-sm">
        <thead>
          <tr className="bg-surface-card">
            <th className="px-4 py-3 text-left text-xs font-medium text-text-secondary">Role</th>
            {OPERATIONS.map((op) => (
              <th key={op} className="px-4 py-3 text-left text-xs font-medium text-text-secondary">
                {op}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {roles.map((role) => (
            <tr key={role} className="border-t border-border-primary">
              <th scope="row" className="px-4 py-3 text-left font-mono text-text-primary font-medium">
                {role}
              </th>
              {OPERATIONS.map((op) => {
                const { label, className, Icon } = STATUS[cellStatus(op, permissionFor(doc, table, role, op))];
                return (
                  <td key={op} className="px-4 py-2">
                    <button
                      type="button"
                      onClick={() => onOpen(role, op)}
                      disabled={disabled}
                      aria-label={`${role} ${op}: ${label}`}
                      data-testid={`perm-cell-${role}-${op}`}
                      className={`flex items-center gap-1.5 px-2.5 py-1 rounded-md border text-xs hover:bg-surface-hover disabled:cursor-not-allowed ${className}`}
                    >
                      <Icon className="w-3.5 h-3.5" />
                      {label}
                    </button>
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
