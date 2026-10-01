import { useState } from 'react';
import { toast } from 'sonner';
import { SidePanel } from '../ui/SidePanel';
import { ConfirmModal } from '../ui/ConfirmModal';
import { apiErrorMessage, type AnyPermission, type Operation } from '../../api/permissions';
import { useDeleteTablePermission, useSaveTablePermission } from '../../hooks/usePermissions';
import { BoolExpEditor } from './BoolExpEditor';
import { ColumnChecklist } from './ColumnChecklist';
import { PresetsEditor } from './PresetsEditor';
import {
  buildPermission,
  formErrors,
  hasErrors,
  initialForm,
  usesCheck,
  usesColumns,
  usesFilter,
  usesPresets,
  type PermissionForm,
} from './permissionForm';

interface PermissionEditorProps {
  readonly projectId: string;
  readonly table: string;
  readonly role: string;
  readonly operation: Operation;
  readonly existing: AnyPermission | undefined;
  readonly columns: string[];
  readonly onClose: () => void;
}

const FILTER_HELP: Record<Operation, string> = {
  select: 'Which rows the role can read.',
  insert: '',
  update: 'Which rows the role can change (they must also pass its select filter).',
  delete: 'Which rows the role can delete (they must also pass its select filter).',
};

const CHECK_HELP: Partial<Record<Operation, string>> = {
  insert: 'What a new row must satisfy; checked inside the same transaction.',
  update: 'What the row must satisfy after the change.',
};

const COLUMNS_LEGEND: Partial<Record<Operation, string>> = {
  select: 'Columns the role can read',
  insert: 'Columns the role can set',
  update: 'Columns the role can change',
};

/** Edits one (table, role, operation) permission. Save is PUT, remove is DELETE. */
export function PermissionEditor({ projectId, table, role, operation, existing, columns, onClose }: PermissionEditorProps) {
  const [form, setForm] = useState<PermissionForm>(() => initialForm(operation, existing));
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [serverError, setServerError] = useState<string | null>(null);
  const save = useSaveTablePermission(projectId);
  const remove = useDeleteTablePermission(projectId);
  const errors = formErrors(operation, form);
  const patch = (next: Partial<PermissionForm>) => setForm((current) => ({ ...current, ...next }));

  const handleSave = () => {
    setServerError(null);
    save.mutate(
      { table, role, operation, permission: buildPermission(operation, form) },
      {
        onSuccess: () => {
          toast.success(`Saved ${role} ${operation} permission on ${table}`);
          onClose();
        },
        onError: (err) => setServerError(apiErrorMessage(err, 'The permission could not be saved.')),
      },
    );
  };

  const handleRemove = () => {
    setConfirmRemove(false);
    setServerError(null);
    remove.mutate(
      { table, role, operation },
      {
        onSuccess: () => {
          toast.success(`Removed ${role} ${operation} permission on ${table}`);
          onClose();
        },
        onError: (err) => setServerError(apiErrorMessage(err, 'The permission could not be removed.')),
      },
    );
  };

  const footer = (
    <div className="space-y-2">
      {serverError && (
        <p role="alert" className="text-xs text-red-400" data-testid="permission-save-error">
          {serverError}
        </p>
      )}
      <div className="flex gap-2">
        {existing && (
          <button
            type="button"
            onClick={() => setConfirmRemove(true)}
            disabled={remove.isPending}
            className="px-3 py-2 text-sm rounded-lg text-red-400 hover:bg-red-500/10 disabled:opacity-50"
          >
            Remove permission
          </button>
        )}
        <button
          type="button"
          onClick={handleSave}
          disabled={hasErrors(errors) || save.isPending}
          className="flex-1 px-4 py-2 bg-purple-500 hover:bg-purple-600 text-white text-sm font-medium rounded-lg disabled:opacity-50"
        >
          {save.isPending ? 'Saving...' : 'Save permission'}
        </button>
      </div>
    </div>
  );

  return (
    <SidePanel open onClose={onClose} title={`${role} · ${operation} on ${table}`} width="w-[520px]" footer={footer}>
      <div className="space-y-5">
        {usesFilter(operation) && (
          <BoolExpEditor
            id="filter"
            label="Row filter"
            help={FILTER_HELP[operation]}
            value={form.filter}
            error={errors.filter}
            columns={columns}
            onChange={(filter) => patch({ filter })}
          />
        )}
        {usesCheck(operation) && (
          <BoolExpEditor
            id="check"
            label="Row check"
            help={CHECK_HELP[operation] ?? ''}
            value={form.check}
            error={errors.check}
            columns={columns}
            onChange={(check) => patch({ check })}
          />
        )}
        {usesColumns(operation) && (
          <ColumnChecklist
            legend={COLUMNS_LEGEND[operation] ?? 'Columns'}
            columns={columns}
            value={form.columns}
            error={errors.columns}
            onChange={(value) => patch({ columns: value })}
          />
        )}
        {operation === 'select' && <SelectOptions form={form} error={errors.limit} onChange={patch} />}
        {usesPresets(operation) && (
          <PresetsEditor
            columns={columns}
            rows={form.presets}
            error={errors.presets}
            onChange={(presets) => patch({ presets })}
          />
        )}
        <p className="text-xs text-text-tertiary">
          Session variables: <code>X-Excalibase-User-Id</code>, <code>X-Excalibase-Role</code>,{' '}
          <code>X-Excalibase-Email</code>, or <code>X-Excalibase-&lt;claim&gt;</code> for any other token claim. A
          request without a variable the permission uses is refused.
        </p>
      </div>
      <ConfirmModal
        open={confirmRemove}
        onClose={() => setConfirmRemove(false)}
        onConfirm={handleRemove}
        title="Remove permission"
        message={`${role} will no longer be able to ${operation} on ${table}.`}
        confirmLabel="Remove"
        destructive
      />
    </SidePanel>
  );
}

interface SelectOptionsProps {
  readonly form: PermissionForm;
  readonly error?: string;
  readonly onChange: (patch: Partial<PermissionForm>) => void;
}

function SelectOptions({ form, error, onChange }: SelectOptionsProps) {
  return (
    <div className="space-y-3">
      <div>
        <label htmlFor="perm-limit" className="block text-sm font-medium text-text-secondary mb-1">
          Row limit
        </label>
        <input
          id="perm-limit"
          type="number"
          min={1}
          value={form.limit}
          onChange={(e) => onChange({ limit: e.target.value })}
          placeholder="none"
          className="w-40 px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm"
        />
        {error && (
          <p className="text-xs text-red-400 mt-1" data-testid="limit-error">
            {error}
          </p>
        )}
      </div>
      <label className="flex items-center gap-2 text-sm text-text-secondary">
        <input
          type="checkbox"
          checked={form.allowAggregations}
          onChange={(e) => onChange({ allowAggregations: e.target.checked })}
          className="rounded"
        />
        Allow aggregations
      </label>
    </div>
  );
}
