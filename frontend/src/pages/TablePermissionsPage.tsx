import { useState } from 'react';
import { Link, useParams } from 'react-router-dom';
import { ArrowLeft, Info, Loader2, Plus, ShieldCheck } from 'lucide-react';
import { useColumns } from '../hooks/useSchema';
import { usePermissionDocument } from '../hooks/usePermissions';
import { useProjectRole } from '../hooks/useProjectRole';
import { PermissionGrid } from '../components/permissions/PermissionGrid';
import { PermissionEditor } from '../components/permissions/PermissionEditor';
import { apiErrorMessage, type Operation } from '../api/permissions';
import { gridRoles, permissionFor, tableKey } from '../utils/permissionModel';
import { validatePermissionRole } from '../utils/permissionRules';

/** Hasura-style permissions of one table: a role × operation grid (EXC-370). */
export function TablePermissionsPage() {
  const { projectId = '', schema = 'public', table = '' } = useParams();
  const key = tableKey(schema, table);
  const { canDevelop, isLoading: roleLoading } = useProjectRole(projectId);
  const doc = usePermissionDocument(projectId, canDevelop);
  const { data: columnInfo = [] } = useColumns(projectId, table, schema);
  const columns = columnInfo.map((c) => c.name);
  const [addedRoles, setAddedRoles] = useState<string[]>([]);
  const [editing, setEditing] = useState<{ role: string; operation: Operation } | null>(null);
  const roles = gridRoles(doc.data, key, addedRoles);

  return (
    <div className="space-y-5 max-w-5xl" data-testid="table-permissions-page">
      <div className="flex items-center gap-3">
        <Link
          to={`/project/${projectId}/database/tables`}
          className="p-1.5 rounded-lg text-text-tertiary hover:text-text-primary hover:bg-surface-hover"
          aria-label="Back to tables"
        >
          <ArrowLeft className="w-4 h-4" />
        </Link>
        <ShieldCheck className="w-5 h-5 text-purple-400" />
        <h3 className="text-lg font-semibold text-text-primary">
          API permissions <span className="font-mono text-text-secondary">{key}</span>
        </h3>
      </div>

      <div data-testid="permissions-notes" className="flex gap-3 rounded-lg border border-blue-500/30 bg-blue-500/5 px-4 py-3 text-xs text-text-secondary">
        <Info className="w-4 h-4 text-blue-400 flex-shrink-0 mt-0.5" />
        <div className="space-y-1">
          <p>
            A table with no permission for a role does not exist for that role: it is not in its GraphQL schema, REST
            answers 404 and realtime refuses the subscription.
          </p>
          <p>
            <code>anon</code> is a request without a token (or with a publishable key), <code>user</code> a signed-in
            end user. <code>service</code> (secret API keys) bypasses every permission.
          </p>
        </div>
      </div>

      <PageBody
        roleLoading={roleLoading}
        canDevelop={canDevelop}
        docLoading={doc.isLoading}
        docError={doc.error}
      >
        <PermissionGrid doc={doc.data} table={key} roles={roles} onOpen={(role, operation) => setEditing({ role, operation })} />
        <AddRoleForm roles={roles} onAdd={(role) => setAddedRoles((current) => [...current, role])} />
      </PageBody>

      {editing && (
        <PermissionEditor
          key={`${editing.role}-${editing.operation}`}
          projectId={projectId}
          table={key}
          role={editing.role}
          operation={editing.operation}
          existing={permissionFor(doc.data, key, editing.role, editing.operation)}
          columns={columns}
          onClose={() => setEditing(null)}
        />
      )}
    </div>
  );
}

interface PageBodyProps {
  readonly roleLoading: boolean;
  readonly canDevelop: boolean;
  readonly docLoading: boolean;
  readonly docError: Error | null;
  readonly children: React.ReactNode;
}

function PageBody({ roleLoading, canDevelop, docLoading, docError, children }: PageBodyProps) {
  if (roleLoading || (canDevelop && docLoading)) {
    return (
      <div className="flex justify-center py-12">
        <Loader2 className="w-6 h-6 animate-spin text-purple-400" />
      </div>
    );
  }
  if (!canDevelop) {
    return (
      <p className="text-sm text-text-tertiary">
        API permissions can be viewed and changed by project developers and above.
      </p>
    );
  }
  if (docError) {
    return (
      <p role="alert" className="text-sm text-red-400">
        {apiErrorMessage(docError, 'The permissions could not be read.')}
      </p>
    );
  }
  return <>{children}</>;
}

interface AddRoleFormProps {
  readonly roles: string[];
  readonly onAdd: (role: string) => void;
}

function AddRoleForm({ roles, onAdd }: AddRoleFormProps) {
  const [name, setName] = useState('');
  const [error, setError] = useState<string | null>(null);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const role = name.trim();
    const problem = validatePermissionRole(role) ?? (roles.includes(role) ? `${role} is already in the grid` : null);
    setError(problem);
    if (problem) return;
    onAdd(role);
    setName('');
  };

  return (
    <form onSubmit={submit} className="space-y-1">
      <div className="flex items-center gap-2">
        <input
          aria-label="New role"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="custom role, e.g. editor"
          className="w-64 px-3 py-2 rounded-lg border border-border-primary bg-bg-primary text-text-primary text-sm font-mono focus:outline-none focus:ring-2 focus:ring-purple-500"
        />
        <button
          type="submit"
          className="flex items-center gap-1 px-3 py-2 text-sm rounded-lg text-purple-400 hover:bg-purple-500/10"
        >
          <Plus className="w-4 h-4" /> Add role
        </button>
      </div>
      <p className="text-xs text-text-tertiary">
        A custom role is one an end user's token carries (set it on the Auth users page). An added role stays in the grid
        once it has a permission on this table.
      </p>
      {error && (
        <p className="text-xs text-red-400" data-testid="add-role-error">
          {error}
        </p>
      )}
    </form>
  );
}
