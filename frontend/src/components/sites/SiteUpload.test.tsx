import { describe, test, expect, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { gunzipSync } from 'node:zlib';
import { SiteUpload } from './SiteUpload';

const picked = (path: string, body: string) => {
  const file = new File([body], path.split('/').pop() ?? path);
  Object.defineProperty(file, 'webkitRelativePath', { value: path });
  return file;
};

describe('SiteUpload', () => {
  test('sends a picked archive as it is', async () => {
    const user = userEvent.setup();
    const onDeploy = vi.fn().mockResolvedValue(undefined);
    render(<SiteUpload deploying={false} onDeploy={onDeploy} />);
    const archive = new File(['gz'], 'dist.tgz', { type: 'application/gzip' });
    await user.upload(screen.getByTestId('site-upload-file'), archive);
    await waitFor(() => expect(onDeploy).toHaveBeenCalledWith(archive));
    expect(await screen.findByTestId('site-upload-done')).toBeInTheDocument();
  });

  test('packs a picked folder into a gzipped tar with index.html at its root', async () => {
    const user = userEvent.setup();
    const onDeploy = vi.fn().mockResolvedValue(undefined);
    render(<SiteUpload deploying={false} onDeploy={onDeploy} />);
    const folder = screen.getByTestId('site-upload-folder');
    expect(folder).toHaveAttribute('webkitdirectory');
    await user.upload(folder, [
      picked('dist/index.html', '<h1>hi</h1>'),
      picked('dist/app.js', 'x'),
    ]);
    await waitFor(() => expect(onDeploy).toHaveBeenCalledTimes(1));
    const blob = onDeploy.mock.calls[0][0] as Blob;
    expect(blob.type).toBe('application/gzip');
    const tar = gunzipSync(new Uint8Array(await blob.arrayBuffer()));
    expect(tar.subarray(0, 10).toString()).toBe('index.html');
  });

  test('says why a folder could not be packed, without deploying', async () => {
    const user = userEvent.setup();
    const onDeploy = vi.fn();
    render(<SiteUpload deploying={false} onDeploy={onDeploy} />);
    await user.upload(screen.getByTestId('site-upload-folder'), [
      picked(`dist/${'d'.repeat(200)}/${'f'.repeat(120)}`, 'x'),
    ]);
    expect(await screen.findByTestId('site-upload-error')).toHaveTextContent(/too long/);
    expect(onDeploy).not.toHaveBeenCalled();
  });

  test('disables both pickers while a deploy is publishing', () => {
    render(<SiteUpload deploying onDeploy={vi.fn()} />);
    expect(screen.getByTestId('site-upload-file')).toBeDisabled();
    expect(screen.getByTestId('site-upload-folder')).toBeDisabled();
    expect(screen.getByTestId('site-upload-busy')).toHaveTextContent('Publishing');
  });
});
