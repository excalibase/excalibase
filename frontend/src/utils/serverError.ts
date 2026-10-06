import { isAxiosError } from 'axios';

interface FailedResponse {
  status?: number;
  data?: unknown;
}

// What a user reads when a call fails. The API answers refusals as
// {"error": "<reason>"}; that reason is shown as is. Axios's own text
// ("Request failed with status code 400") never reaches the screen.
export function serverErrorMessage(err: unknown, fallback: string): string {
  const response = (err as { response?: FailedResponse } | null | undefined)?.response;
  if (response) {
    const reason = (response.data as { error?: unknown } | undefined)?.error;
    if (typeof reason === 'string' && reason.trim() !== '') return reason;
    if ((response.status ?? 0) >= 500) return `${fallback}: the server hit an error. Try again in a moment.`;
    return fallback;
  }
  if (isAxiosError(err)) return `${fallback}: the server could not be reached.`;
  if (err instanceof Error && err.message) return err.message;
  return fallback;
}
