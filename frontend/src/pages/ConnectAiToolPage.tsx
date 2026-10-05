import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { Bot, Check, Copy, Loader2 } from 'lucide-react';
import { Button } from '../components/Button';
import { useCreateAccessToken, type CreatedAccessToken, type TokenAccess } from '../api/accessTokens';
import { AI_TOOLS, mcpSetup, mcpUrl, type AiToolId } from '../utils/mcpConfig';

const FIELD = 'px-3 py-2 bg-bg-secondary border border-border-primary rounded-lg text-text-primary';

const EXPIRY_OPTIONS = [
  { value: '30d', label: '30 days' },
  { value: '90d', label: '90 days' },
  { value: '365d', label: '1 year' },
];

function errorText(err: unknown): string {
  const axiosErr = err as { response?: { data?: { error?: string } } };
  return axiosErr?.response?.data?.error || 'Could not create the token';
}

function CopyCode({ code }: { readonly code: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(code);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  };
  return (
    <div className="flex items-start gap-2">
      <pre className="flex-1 p-3 rounded bg-bg-secondary text-xs text-text-primary overflow-x-auto whitespace-pre-wrap break-all">{code}</pre>
      <button type="button" onClick={copy} aria-label="Copy" className="p-2 text-text-secondary hover:text-text-primary">
        {copied ? <Check className="w-4 h-4" /> : <Copy className="w-4 h-4" />}
      </button>
    </div>
  );
}

function SetupSteps({ tool, url, token, onDone }: {
  readonly tool: AiToolId;
  readonly url: string;
  readonly token: CreatedAccessToken;
  readonly onDone: () => void;
}) {
  return (
    <div data-testid="mcp-setup" className="space-y-4 p-4 rounded-lg border border-purple-500/30 bg-purple-500/10">
      <p className="text-sm text-text-primary">
        The token <strong>{token.name}</strong> is shown once. Copy the first step now.
      </p>
      {mcpSetup(tool, url, token.token).map((step) => (
        <div key={step.label} className="space-y-1">
          <p className="text-sm text-text-secondary">
            {step.label}{step.file && <> in <code className="text-text-primary">{step.file}</code></>}
          </p>
          <CopyCode code={step.code} />
        </div>
      ))}
      <Button type="button" onClick={onDone}>I have saved it</Button>
    </div>
  );
}

function ChoiceGroup<T extends string>({ legend, name, options, value, onChange }: {
  readonly legend: string;
  readonly name: string;
  readonly options: readonly { readonly id: T; readonly label: string }[];
  readonly value: T;
  readonly onChange: (value: T) => void;
}) {
  return (
    <fieldset>
      <legend className="block text-sm text-text-secondary mb-1">{legend}</legend>
      <div className="flex flex-wrap gap-2">
        {options.map((option) => (
          <label key={option.id} className={`${FIELD} flex items-center gap-2 cursor-pointer`}>
            <input type="radio" name={name} value={option.id} checked={value === option.id} onChange={() => onChange(option.id)} />
            {option.label}
          </label>
        ))}
      </div>
    </fieldset>
  );
}

const ACCESS_OPTIONS: readonly { id: TokenAccess; label: string }[] = [
  { id: 'read', label: 'Read only' },
  { id: 'write', label: 'Read and write' },
];

export function ConnectAiToolPage() {
  const { projectId = '' } = useParams<{ projectId: string }>();
  const createToken = useCreateAccessToken();
  const [tool, setTool] = useState<AiToolId>('cursor');
  const [access, setAccess] = useState<TokenAccess>('read');
  const [expiresIn, setExpiresIn] = useState('90d');
  const [created, setCreated] = useState<{ tool: AiToolId; url: string; token: CreatedAccessToken } | null>(null);
  const [error, setError] = useState<string | null>(null);

  const toolLabel = AI_TOOLS.find((option) => option.id === tool)?.label ?? tool;

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    try {
      const token = await createToken.mutateAsync({ name: `${toolLabel} MCP`, access, expiresIn, projectId });
      // Only the setup panel keeps the secret; the mutation forgets it.
      createToken.reset();
      setCreated({ tool, url: mcpUrl(window.location.origin, projectId, access === 'read'), token });
    } catch (err) {
      setError(errorText(err));
    }
  };

  return (
    <div className="space-y-6 max-w-4xl" data-testid="connect-ai-tool-page">
      <div>
        <h3 className="flex items-center gap-2 text-lg font-semibold text-text-primary"><Bot className="w-5 h-5" /> Connect your AI tool</h3>
        <p className="text-sm text-text-secondary mt-1">
          Let an AI coding tool read this project's schema, run SQL and migrations, generate types and deploy, over MCP.
          It acts with a new personal access token bound to this project, never with more than your own role allows.
          Read only lets it look and query, never change anything.
        </p>
      </div>

      {error && <div className="px-4 py-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-400 text-sm">{error}</div>}

      {created ? (
        <SetupSteps tool={created.tool} url={created.url} token={created.token} onDone={() => setCreated(null)} />
      ) : (
        <form onSubmit={submit} className="space-y-4">
          <ChoiceGroup legend="Tool" name="ai-tool" options={AI_TOOLS} value={tool} onChange={setTool} />
          <ChoiceGroup legend="Access" name="ai-access" options={ACCESS_OPTIONS} value={access} onChange={setAccess} />
          <div>
            <label htmlFor="ai-token-expires" className="block text-sm text-text-secondary mb-1">Token expires</label>
            <select id="ai-token-expires" value={expiresIn} onChange={(e) => setExpiresIn(e.target.value)} className={FIELD}>
              {EXPIRY_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
            </select>
          </div>
          <Button type="submit" disabled={createToken.isPending} className="flex items-center gap-2">
            {createToken.isPending && <Loader2 className="w-4 h-4 animate-spin" />}
            Create token and show setup
          </Button>
        </form>
      )}
    </div>
  );
}
