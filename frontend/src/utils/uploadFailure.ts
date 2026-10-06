import { isAxiosError } from 'axios';

// The three requests an upload makes: mint a signed URL, PUT the bytes to the
// object store, confirm the staged upload.
export type UploadStep = 'sign' | 'put' | 'confirm';

const STEP_NAMES: Record<UploadStep, string> = {
  sign: 'getting an upload URL',
  put: 'sending the file to storage',
  confirm: 'confirming the upload',
};

// An upload failure that knows which request failed and with what status.
export class UploadStepError extends Error {
  readonly step: UploadStep;
  readonly status?: number;

  constructor(step: UploadStep, reason: string, status?: number) {
    super(`while ${STEP_NAMES[step]}: ${reason}`);
    this.name = 'UploadStepError';
    this.step = step;
    this.status = status;
  }
}

const XML_ENTITIES: Record<string, string> = { '&lt;': '<', '&gt;': '>', '&amp;': '&', '&quot;': '"', '&apos;': "'" };

function xmlText(body: string, tag: string): string | null {
  const match = new RegExp(`<${tag}>([^<]*)</${tag}>`).exec(body);
  if (!match) return null;
  return match[1].replace(/&(lt|gt|amp|quot|apos);/g, (entity) => XML_ENTITIES[entity]).trim();
}

// storageRefusal reads an S3 / R2 error body ("<Error><Code>…</Code>
// <Message>…</Message></Error>") into "Code: Message", or null.
export function storageRefusal(body: unknown): string | null {
  if (typeof body !== 'string' || !body.includes('<Error>')) return null;
  const code = xmlText(body, 'Code');
  if (!code) return null;
  const message = xmlText(body, 'Message');
  return message ? `${code}: ${message}` : code;
}

function apiReason(data: unknown): string | null {
  const reason = (data as { error?: unknown } | null | undefined)?.error;
  return typeof reason === 'string' && reason.trim() !== '' ? reason : null;
}

// uploadFailure turns whatever a step threw into an UploadStepError that
// names the step and, when there was an answer, its HTTP status and reason.
export function uploadFailure(step: UploadStep, err: unknown): UploadStepError {
  if (isAxiosError(err)) {
    const response = err.response;
    if (!response) {
      const unreachable = step === 'put'
        ? 'the storage service could not be reached (a network or CORS refusal)'
        : 'the server could not be reached';
      return new UploadStepError(step, unreachable);
    }
    const reason = step === 'put' ? storageRefusal(response.data) : apiReason(response.data);
    const status = `HTTP ${response.status}`;
    return new UploadStepError(step, reason ? `${status}, ${reason}` : status, response.status);
  }
  if (err instanceof Error && err.message) return new UploadStepError(step, err.message);
  return new UploadStepError(step, 'no reason given');
}
