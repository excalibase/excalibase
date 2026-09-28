import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { Database, Loader2 } from 'lucide-react';
import { useAddDatabase, useInstance } from '../hooks/useProvisioning';
import { usePostgresCatalog, findMajor } from '../api/postgresCatalog';
import { PostgresVersionPicker } from '../components/PostgresVersionPicker';
import { apiErrorMessage } from '../api/apps';
import { DOCUMENTDB_LABEL } from '../utils/engine';
import { Button } from '../components/Button';
import { secondaryButton } from '../components/containers/ContainerBits';

type Engine = 'POSTGRESQL' | 'DOCUMENTDB';

const ENGINES: readonly { engine: Engine; icon: string; label: string; desc: string }[] = [
  { engine: 'POSTGRESQL', icon: '🐘', label: 'PostgreSQL', desc: 'CloudNativePG operator' },
  { engine: 'DOCUMENTDB', icon: '🍃', label: DOCUMENTDB_LABEL, desc: 'MongoDB wire protocol on PostgreSQL' },
];

function tileClass(selected: boolean): string {
  return selected
    ? 'border-accent-primary bg-accent-primary/10'
    : 'border-border-primary bg-bg-tertiary hover:border-border-secondary';
}

// Adds the database to a project created without one (EXC-426). The same
// choices as creating a project with a database; the project's plan sizes it
// and decides its backups, exactly as at creation.
export function AddDatabasePage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const { data: project } = useInstance(projectId);
  const catalog = usePostgresCatalog();
  const addDatabase = useAddDatabase(projectId);

  const [engine, setEngine] = useState<Engine>('POSTGRESQL');
  const [postgresVersion, setPostgresVersion] = useState('');
  const [documentDb, setDocumentDb] = useState(false);
  const [failure, setFailure] = useState('');
  const documentDbOnly = engine === 'DOCUMENTDB';

  const chooseEngine = (next: Engine) => {
    setEngine(next);
    setDocumentDb(false);
    if (next === 'DOCUMENTDB' && !findMajor(catalog.data, postgresVersion)?.documentDb) setPostgresVersion('');
  };
  const chooseVersion = (version: string) => {
    setPostgresVersion(version);
    if (!findMajor(catalog.data, version)?.documentDb) setDocumentDb(false);
  };

  if (!project) {
    return (
      <div className="flex justify-center py-16">
        <Loader2 className="w-6 h-6 animate-spin text-text-tertiary" />
      </div>
    );
  }
  if (!project.noDatabase) {
    return (
      <div data-testid="add-database-has-one" className="max-w-xl mx-auto py-16 space-y-4 text-center">
        <p className="text-sm text-text-secondary">This project already has a database.</p>
        <Link to={`/project/${projectId}`} className={secondaryButton}>
          Back to overview
        </Link>
      </div>
    );
  }
  if (!project.canAddDatabase) {
    return (
      <p data-testid="add-database-not-allowed" className="max-w-xl mx-auto py-16 text-sm text-text-secondary text-center">
        This project has no database. Only an org admin or owner can add one.
      </p>
    );
  }

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    setFailure('');
    const result = await addDatabase.mutateAsync({
      databaseType: 'POSTGRESQL',
      postgresVersion,
      documentDb: documentDbOnly || documentDb,
    });
    if (result.noDatabase) {
      setFailure(result.failureReason || 'The database could not be created.');
      return;
    }
    navigate(`/project/${projectId}`);
  };

  const errorText = failure || (addDatabase.error ? apiErrorMessage(addDatabase.error, 'Could not add the database') : '');

  return (
    <form onSubmit={(e) => void handleSubmit(e).catch(() => undefined)} className="max-w-3xl mx-auto space-y-6" data-testid="add-database-page">
      <div>
        <h2 className="text-xl font-bold text-text-primary">Add a database</h2>
        <p className="text-sm text-text-tertiary mt-0.5">
          It runs beside this project&apos;s containers, private by default, sized and backed up by your
          organization&apos;s plan.
        </p>
      </div>
      <div className="grid grid-cols-2 gap-3">
        {ENGINES.map(({ engine: option, icon, label, desc }) => (
          <button key={option} type="button" onClick={() => chooseEngine(option)} aria-pressed={engine === option}
            data-testid={`add-engine-${option}`}
            className={`p-4 rounded-xl border-2 text-left transition-all ${tileClass(engine === option)}`}
          >
            <div className="text-3xl mb-2">{icon}</div>
            <p className="font-semibold text-text-primary text-sm">{label}</p>
            <p className="text-xs text-text-tertiary mt-0.5">{desc}</p>
          </button>
        ))}
      </div>
      <PostgresVersionPicker
        catalog={catalog.data}
        isLoading={catalog.isLoading}
        error={catalog.error}
        version={postgresVersion}
        onVersionChange={chooseVersion}
        documentDb={documentDb}
        onDocumentDbChange={setDocumentDb}
        documentDbOnly={documentDbOnly}
      />
      {errorText && (
        <p role="alert" className="text-sm text-red-400 break-words">
          {errorText}
        </p>
      )}
      <div className="flex gap-3">
        <Link to={`/project/${projectId}`} className={secondaryButton}>
          Cancel
        </Link>
        <Button type="submit" data-testid="add-database-submit" disabled={addDatabase.isPending || !postgresVersion}>
          {addDatabase.isPending
            ? <><Loader2 className="w-4 h-4 mr-2 animate-spin inline" /> Creating the database...</>
            : <><Database className="w-4 h-4 mr-2 inline" /> Add database</>}
        </Button>
      </div>
    </form>
  );
}
