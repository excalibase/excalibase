import { useState } from 'react';
import { FileArchive, FolderUp, Loader2 } from 'lucide-react';
import { apiErrorMessage } from '../../api/apps';
import { secondaryButton } from '../containers/ContainerBits';
import { packFolder } from './tarball';

interface SiteUploadProps {
  readonly deploying: boolean;
  readonly onDeploy: (archive: Blob) => Promise<unknown>;
}

type Outcome = { state: 'idle' } | { state: 'done' } | { state: 'failed'; message: string };

const pickerClass = `${secondaryButton} cursor-pointer`;

// A deploy straight from the browser: a built dist.tgz, or the dist folder packed here.
export function SiteUpload({ deploying, onDeploy }: SiteUploadProps) {
  const [packing, setPacking] = useState(false);
  const [outcome, setOutcome] = useState<Outcome>({ state: 'idle' });
  const busy = deploying || packing;

  const pack = async (archive: () => Promise<Blob>): Promise<Blob | null> => {
    setPacking(true);
    try {
      return await archive();
    } catch (err) {
      setOutcome({
        state: 'failed',
        message: err instanceof Error ? err.message : 'The folder could not be packed',
      });
      return null;
    } finally {
      setPacking(false);
    }
  };

  const send = async (archive: () => Promise<Blob>) => {
    setOutcome({ state: 'idle' });
    const body = await pack(archive);
    if (!body) return;
    try {
      await onDeploy(body);
      setOutcome({ state: 'done' });
    } catch (err) {
      setOutcome({ state: 'failed', message: apiErrorMessage(err, 'The deploy failed') });
    }
  };

  const onFile = (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = '';
    if (file) void send(async () => file);
  };

  const onFolder = (event: React.ChangeEvent<HTMLInputElement>) => {
    const files = [...(event.target.files ?? [])];
    event.target.value = '';
    if (files.length > 0) void send(() => packFolder(files));
  };

  return (
    <section
      className="bg-surface-card border border-border-primary rounded-lg p-4 space-y-3"
      data-testid="site-upload"
    >
      <p className="text-sm text-text-secondary">
        Upload your build output: a <span className="font-mono">.tar.gz</span> with{' '}
        <span className="font-mono">index.html</span> at its root, or the build folder itself. It
        goes live as soon as it is published.
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <label className={pickerClass} aria-disabled={busy}>
          <FileArchive className="w-4 h-4" /> Upload .tar.gz
          <input
            type="file"
            accept=".tar.gz,.tgz,.tar,application/gzip,application/x-tar"
            className="sr-only"
            disabled={busy}
            onChange={onFile}
            data-testid="site-upload-file"
          />
        </label>
        <label className={pickerClass} aria-disabled={busy}>
          <FolderUp className="w-4 h-4" /> Upload folder
          <input
            type="file"
            className="sr-only"
            disabled={busy}
            onChange={onFolder}
            data-testid="site-upload-folder"
            {...{ webkitdirectory: '', directory: '' }}
          />
        </label>
        {busy && (
          <span
            className="inline-flex items-center gap-2 text-xs text-text-tertiary"
            data-testid="site-upload-busy"
          >
            <Loader2 className="w-3.5 h-3.5 animate-spin" />{' '}
            {packing ? 'Packing the folder' : 'Publishing'}
          </span>
        )}
      </div>
      {outcome.state === 'done' && (
        <p className="text-sm text-green-400" data-testid="site-upload-done">
          The deploy is live.
        </p>
      )}
      {outcome.state === 'failed' && (
        <p role="alert" className="text-sm text-red-400" data-testid="site-upload-error">
          {outcome.message}
        </p>
      )}
    </section>
  );
}
