import { useState, useRef, useCallback } from 'react';
import { useParams } from 'react-router-dom';
import {
  useBuckets,
  useCreateBucket,
  useDeleteBucket,
  useObjects,
  useUploadFile,
  useDeleteObject,
  useDownloadURL,
} from '../hooks/useStorage';
import { Button } from '../components/Button';
import { Loader2, FolderPlus, Upload, Trash2, Download, Globe, Lock } from 'lucide-react';

export function StoragePage() {
  const { projectId } = useParams<{ projectId: string }>();
  const [selectedBucket, setSelectedBucket] = useState<string | null>(null);
  const [showCreate, setShowCreate] = useState(false);

  const { data: buckets = [], isLoading: bucketsLoading } = useBuckets(projectId!);

  if (!selectedBucket && buckets.length > 0) {
    // eslint-disable-next-line react-hooks/rules-of-hooks
    setTimeout(() => setSelectedBucket(buckets[0].name), 0);
  }

  return (
    <div className="max-w-7xl mx-auto h-full flex gap-4">
      <aside className="w-60 bg-surface-card border border-border-primary rounded-xl flex flex-col">
        <div className="px-4 py-3 border-b border-border-primary flex items-center justify-between">
          <h2 className="font-semibold">Buckets</h2>
          <Button size="sm" variant="ghost" onClick={() => setShowCreate(true)} title="Create bucket">
            <FolderPlus className="w-4 h-4" />
          </Button>
        </div>
        <div className="flex-1 overflow-y-auto p-2">
          {bucketsLoading && (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="w-4 h-4 animate-spin text-accent-primary" />
            </div>
          )}
          {!bucketsLoading && buckets.length === 0 && (
            <p className="text-sm text-text-tertiary px-2 py-3">
              No buckets yet. Create one to start uploading files.
            </p>
          )}
          {!bucketsLoading && buckets.length > 0 && buckets.map((b) => (
            <button
              key={b.id}
              onClick={() => setSelectedBucket(b.name)}
              className={`w-full text-left px-3 py-2 rounded-lg text-sm transition-colors flex items-center gap-2 ${
                selectedBucket === b.name
                  ? 'bg-accent-primary/10 text-accent-primary'
                  : 'text-text-secondary hover:bg-surface-hover'
              }`}
            >
              {b.public ? <Globe className="w-3.5 h-3.5" /> : <Lock className="w-3.5 h-3.5" />}
              <span className="flex-1 truncate">{b.name}</span>
            </button>
          ))}
        </div>
      </aside>

      <main className="flex-1 bg-surface-card border border-border-primary rounded-xl flex flex-col min-w-0">
        {selectedBucket ? (
          <ObjectBrowser projectId={projectId!} bucket={selectedBucket} buckets={buckets} />
        ) : (
          <div className="flex-1 flex items-center justify-center text-text-tertiary">
            Select a bucket from the left to browse files.
          </div>
        )}
      </main>

      {showCreate && (
        <CreateBucketModal projectId={projectId!} onClose={() => setShowCreate(false)} onCreated={(name) => {
          setSelectedBucket(name);
          setShowCreate(false);
        }} />
      )}
    </div>
  );
}

interface BucketInfo {
  name: string;
  public: boolean;
}

interface ObjectBrowserProps {
  readonly projectId: string;
  readonly bucket: string;
  readonly buckets: BucketInfo[];
}

function ObjectBrowser({ projectId, bucket, buckets }: ObjectBrowserProps) {
  const { data, isLoading } = useObjects(projectId, bucket);
  const upload = useUploadFile(projectId, bucket);
  const del = useDeleteObject(projectId, bucket);
  const download = useDownloadURL(projectId, bucket);
  const delBucket = useDeleteBucket(projectId);
  const fileInput = useRef<HTMLInputElement>(null);
  const [dragOver, setDragOver] = useState(false);
  const isPublic = buckets.find((b) => b.name === bucket)?.public ?? false;

  const handleFiles = useCallback(
    async (files: FileList) => {
      for (const file of Array.from(files)) {
        try {
          await upload.mutateAsync({ file, key: file.name });
        } catch (e: unknown) {
          const err = e as { response?: { data?: { error?: string } }; message?: string };
          alert(`Upload of ${file.name} failed: ${err?.response?.data?.error ?? err?.message ?? 'unknown'}`);
        }
      }
    },
    [upload],
  );

  const handleDownload = async (key: string) => {
    try {
      const resp = await download.mutateAsync(key);
      globalThis.open(resp.url, '_blank');
    } catch (e: unknown) {
      const err = e as { response?: { data?: { error?: string } }; message?: string };
      alert(`Download URL failed: ${err?.response?.data?.error ?? err?.message ?? 'unknown'}`);
    }
  };

  const handleDelete = async (key: string) => {
    if (!confirm(`Delete ${key}? This cannot be undone.`)) return;
    try {
      await del.mutateAsync(key);
    } catch (e: unknown) {
      const err = e as { response?: { data?: { error?: string } }; message?: string };
      alert(`Delete failed: ${err?.response?.data?.error ?? err?.message ?? 'unknown'}`);
    }
  };

  return (
    <>
      <div className="px-6 py-4 border-b border-border-primary flex items-center justify-between">
        <div className="flex items-center gap-2">
          {isPublic ? <Globe className="w-4 h-4" /> : <Lock className="w-4 h-4" />}
          <h2 className="font-semibold">{bucket}</h2>
          <span className="text-xs text-text-tertiary">
            {isPublic ? 'public — anyone can read' : 'private — signed URLs only'}
          </span>
        </div>
        <div className="flex gap-2">
          <input
            ref={fileInput}
            type="file"
            multiple
            className="hidden"
            onChange={(e) => e.target.files && handleFiles(e.target.files)}
          />
          <Button size="sm" onClick={() => fileInput.current?.click()} disabled={upload.isPending}>
            <Upload className="w-3.5 h-3.5 mr-1" />
            {upload.isPending ? 'Uploading…' : 'Upload'}
          </Button>
          <Button
            size="sm"
            variant="danger"
            onClick={async () => {
              if (!confirm(`Delete bucket "${bucket}" and all its files?`)) return;
              await delBucket.mutateAsync(bucket);
            }}
          >
            Delete bucket
          </Button>
        </div>
      </div>

      <section
        aria-label="File drop zone"
        className={`flex-1 overflow-auto p-6 transition-colors ${dragOver ? 'bg-accent-primary/5' : ''}`}
        onDragOver={(e) => {
          e.preventDefault();
          setDragOver(true);
        }}
        onDragLeave={() => setDragOver(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDragOver(false);
          if (e.dataTransfer.files) handleFiles(e.dataTransfer.files);
        }}
      >
        {isLoading && (
          <div className="flex items-center justify-center py-12">
            <Loader2 className="w-5 h-5 animate-spin text-accent-primary" />
          </div>
        )}
        {!isLoading && !data?.objects.length && (
          <div className="text-center py-16 text-text-tertiary">
            <p className="mb-2">{dragOver ? 'Drop to upload' : 'No files yet — drag files here or click Upload.'}</p>
          </div>
        )}
        {!isLoading && data?.objects.length && (
          <table className="w-full text-sm">
            <thead>
              <tr className="text-text-tertiary border-b border-border-primary">
                <th className="text-left px-3 py-2 font-medium">Name</th>
                <th className="text-right px-3 py-2 font-medium">Size</th>
                <th className="text-left px-3 py-2 font-medium">Type</th>
                <th className="text-left px-3 py-2 font-medium">Uploaded</th>
                <th className="px-3 py-2" />
              </tr>
            </thead>
            <tbody>
              {data.objects.map((o) => (
                <tr key={o.id} className="border-b border-border-primary last:border-b-0 hover:bg-bg-hover">
                  <td className="px-3 py-2 font-mono text-xs">{o.key}</td>
                  <td className="px-3 py-2 text-right text-text-secondary">{formatBytes(o.size)}</td>
                  <td className="px-3 py-2 text-text-secondary">{o.mimeType ?? '—'}</td>
                  <td className="px-3 py-2 text-text-secondary">
                    {new Date(o.createdAt).toLocaleString()}
                  </td>
                  <td className="px-3 py-2 text-right">
                    <Button size="sm" variant="ghost" onClick={() => handleDownload(o.key)} title="Download">
                      <Download className="w-3.5 h-3.5" />
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => handleDelete(o.key)} title="Delete">
                      <Trash2 className="w-3.5 h-3.5" />
                    </Button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        ) || null}
      </section>
    </>
  );
}

interface CreateBucketModalProps {
  readonly projectId: string;
  readonly onClose: () => void;
  readonly onCreated: (name: string) => void;
}

function CreateBucketModal({ projectId, onClose, onCreated }: CreateBucketModalProps) {
  const [name, setName] = useState('');
  const [isPublic, setIsPublic] = useState(false);
  const create = useCreateBucket(projectId);

  return (
    <div className="fixed inset-0 z-50 bg-black/50 flex items-center justify-center">
      <button
        type="button"
        className="absolute inset-0 cursor-default"
        aria-label="Close modal"
        onClick={onClose}
      />
      <section
        aria-label="New bucket"
        className="relative bg-surface-card border border-border-primary rounded-xl p-6 w-96 space-y-4"
      >
        <h3 className="text-lg font-semibold">New bucket</h3>
        <input
          type="text"
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="bucket-name"
          autoFocus
          className="w-full px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-sm"
        />
        <p className="text-xs text-text-tertiary">3-63 chars, lowercase, digits, hyphens.</p>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={isPublic} onChange={(e) => setIsPublic(e.target.checked)} />
          <span>Public — anyone can read objects without auth</span>
        </label>
        <div className="flex justify-end gap-2 pt-2">
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            disabled={!name || create.isPending}
            onClick={async () => {
              try {
                const b = await create.mutateAsync({ name, public: isPublic });
                onCreated(b.name);
              } catch (e: unknown) {
                const err = e as { response?: { data?: { error?: string } }; message?: string };
                alert(`Create failed: ${err?.response?.data?.error ?? err?.message ?? 'unknown'}`);
              }
            }}
          >
            {create.isPending ? 'Creating…' : 'Create'}
          </Button>
        </div>
      </section>
    </div>
  );
}

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`;
}
