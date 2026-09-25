import { Link, useNavigate, useParams } from 'react-router-dom';
import {
  apiErrorMessage,
  useApp,
  useCreateApp,
  useUpdateApp,
  type App,
  PartialSaveError,
  type AppSubmission,
} from '../api/apps';
import { useInstance } from '../hooks/useProvisioning';
import { AppForm } from '../components/containers/AppForm';
import { ContainersHeader, HostingGate, Spinner } from '../components/containers/ContainerBits';
import type { TierType } from '../types';
import { toast } from '../utils/toast';

interface FormProps {
  readonly projectId: string;
  readonly tier?: TierType;
  readonly databaseName?: string;
}

function CreateForm({ projectId, tier, databaseName }: FormProps) {
  const navigate = useNavigate();
  const create = useCreateApp(projectId);
  const submit = (submission: AppSubmission) =>
    create.mutate(submission, {
      onSuccess: (app) => navigate(`/project/${projectId}/containers/${app.id}`),
      onError: (err) => {
        // The container exists now; creating it again would be refused, so
        // the missing secret is entered on its edit page instead.
        if (err instanceof PartialSaveError) {
          toast.error(err.message);
          navigate(`/project/${projectId}/containers/${err.app.id}/edit`);
        }
      },
    });

  return (
    <>
      <ContainersHeader
        title="New container"
        subtitle="An image, the port it listens on, and any variables it needs."
      />
      <AppForm
        tier={tier}
        databaseName={databaseName}
        submitLabel="Create container"
        submitting={create.isPending}
        serverError={
          create.error
            ? apiErrorMessage(create.error, 'The container could not be created')
            : undefined
        }
        onSubmit={submit}
        onCancel={() => navigate(`/project/${projectId}/containers`)}
      />
    </>
  );
}

function EditForm({ projectId, app, databaseName }: FormProps & { readonly app: App }) {
  const navigate = useNavigate();
  const update = useUpdateApp(projectId, app.id);
  const detailPath = `/project/${projectId}/containers/${app.id}`;
  const submit = (submission: AppSubmission) =>
    update.mutate(
      { ...submission, version: app.version },
      { onSuccess: () => navigate(detailPath) },
    );

  return (
    <>
      <ContainersHeader
        title={`Edit ${app.name}`}
        subtitle="Changes take effect on the next deploy."
      />
      <AppForm
        tier={app.tier}
        databaseName={databaseName}
        initial={app}
        submitLabel="Save changes"
        submitting={update.isPending}
        serverError={
          update.error ? apiErrorMessage(update.error, 'The changes could not be saved') : undefined
        }
        onSubmit={submit}
        onCancel={() => navigate(detailPath)}
      />
    </>
  );
}

function EditLoader({ projectId, appId, databaseName }: FormProps & { readonly appId: string }) {
  const { data: app, isLoading, error } = useApp(projectId, appId);
  if (isLoading) return <Spinner />;
  if (error || !app) {
    return (
      <div role="alert" className="text-sm text-red-400 py-6">
        {apiErrorMessage(error, 'Could not load this container')}
      </div>
    );
  }
  return <EditForm projectId={projectId} app={app} databaseName={databaseName} />;
}

export function ContainerFormPage() {
  const { projectId = '', appId } = useParams<{ projectId: string; appId?: string }>();
  const { data: instance } = useInstance(projectId);
  const databaseName = instance?.databaseName || undefined;

  return (
    <div data-testid="container-form-page">
      <Link
        to={`/project/${projectId}/containers`}
        className="text-xs text-text-tertiary hover:text-text-primary"
      >
        ← Containers
      </Link>
      <div className="mt-3">
        <HostingGate>
          {appId ? (
            <EditLoader projectId={projectId} appId={appId} databaseName={databaseName} />
          ) : (
            <CreateForm projectId={projectId} tier={instance?.tier} databaseName={databaseName} />
          )}
        </HostingGate>
      </div>
    </div>
  );
}
