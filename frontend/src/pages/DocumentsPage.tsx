import { useEffect, useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import { FileJson, Loader2 } from 'lucide-react';
import { useDocumentDatabases, useProjectIsDocumentDB } from '../hooks/useDocuments';
import { apiErrorMessage } from '../api/documents';
import { CollectionSidebar } from '../components/documents/CollectionSidebar';
import { DocumentList } from '../components/documents/DocumentList';
import { IndexesPanel } from '../components/documents/IndexesPanel';
import { cn } from '../utils/cn';

type Tab = 'documents' | 'indexes';

export function DocumentsPage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const documentDbQuery = useProjectIsDocumentDB(projectId);
  const isDocumentDB = documentDbQuery.data === true;
  const databases = useDocumentDatabases(projectId, isDocumentDB);
  const [database, setDatabase] = useState('');
  const [collection, setCollection] = useState('');
  const [tab, setTab] = useState<Tab>('documents');

  useEffect(() => {
    if (!database && databases.data?.length) setDatabase(databases.data[0]);
  }, [database, databases.data]);

  const collectionRef = useMemo(() => ({ projectId, database, collection }), [projectId, database, collection]);

  if (documentDbQuery.isLoading) return <Loader2 className="w-5 h-5 animate-spin text-text-tertiary" />;
  if (!isDocumentDB) {
    return (
      <p className="text-sm text-text-secondary" data-testid="documents-unavailable">
        Documents are available for projects created with DocumentDB.
      </p>
    );
  }

  const chooseDatabase = (name: string) => {
    setDatabase(name);
    setCollection('');
  };

  return (
    <div data-testid="documents-page">
      <div className="flex items-center gap-3 mb-6">
        <div className="w-10 h-10 rounded-lg bg-purple-500/10 border border-purple-500/30 flex items-center justify-center">
          <FileJson className="w-5 h-5 text-purple-400" />
        </div>
        <div>
          <h3 className="text-lg font-semibold text-text-primary">Documents</h3>
          <p className="text-sm text-text-secondary">Browse and edit the collections served over the MongoDB protocol.</p>
        </div>
      </div>
      {databases.isError && (
        <p role="alert" className="text-sm text-red-400 mb-4">
          {apiErrorMessage(databases.error)}
        </p>
      )}
      <div className="flex gap-6">
        <CollectionSidebar
          projectId={projectId}
          databases={databases.data ?? []}
          database={database}
          collection={collection}
          onDatabase={chooseDatabase}
          onCollection={setCollection}
        />
        <section className="flex-1 min-w-0">
          {collection ? (
            <>
              <div className="flex gap-1 mb-4 border-b border-border-primary" role="tablist">
                {(['documents', 'indexes'] as Tab[]).map((name) => (
                  <button
                    key={name}
                    type="button"
                    role="tab"
                    aria-selected={tab === name}
                    onClick={() => setTab(name)}
                    className={cn(
                      'px-3 py-2 text-sm capitalize -mb-px border-b-2',
                      tab === name ? 'border-purple-400 text-purple-400' : 'border-transparent text-text-secondary',
                    )}
                  >
                    {name}
                  </button>
                ))}
              </div>
              {tab === 'documents' ? (
                <DocumentList key={`${database}/${collection}`} collectionRef={collectionRef} />
              ) : (
                <IndexesPanel collectionRef={collectionRef} />
              )}
            </>
          ) : (
            <p className="text-sm text-text-secondary">Choose a collection.</p>
          )}
        </section>
      </div>
    </div>
  );
}
