import { Download } from 'lucide-react';

// Full verification needs the certificate authority as a file on the client,
// not a value inside the URI. Every login needs TLS, in-cluster ones too, so
// it is offered whether or not the project publishes a port.
export function CertificateAuthority({ projectId, pem }: { readonly projectId: string; readonly pem: string }) {
  if (!pem) return null;
  const href = `data:application/x-pem-file;base64,${btoa(pem)}`;
  return (
    <a
      href={href}
      download={caFileName(projectId)}
      data-testid="conn-ca-download"
      className="inline-flex items-center gap-2 px-3 py-2 rounded border border-border-primary text-sm text-text-secondary hover:text-text-primary hover:border-text-secondary transition-colors"
    >
      <Download className="w-4 h-4" /> Download certificate authority
    </a>
  );
}

// The name the download saves as, so snippets can point at the same file.
export function caFileName(projectId: string): string {
  return `${projectId}-ca.crt`;
}
