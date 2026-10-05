// Setup for the MCP clients Studio supports (EXC-544). The token lives in the
// EXCALIBASE_TOKEN environment variable; config files only reference it, so a
// committed .cursor/mcp.json or .gemini/settings.json carries no secret.

export type AiToolId = 'cursor' | 'claude-code' | 'codex' | 'gemini';

export interface AiTool {
  id: AiToolId;
  label: string;
}

export const AI_TOOLS: readonly AiTool[] = [
  { id: 'cursor', label: 'Cursor' },
  { id: 'claude-code', label: 'Claude Code' },
  { id: 'codex', label: 'Codex' },
  { id: 'gemini', label: 'Gemini CLI' },
];

export interface SetupStep {
  label: string;
  // Where the code goes, when it is a file rather than a command.
  file?: string;
  code: string;
}

const TOKEN_ENV = 'EXCALIBASE_TOKEN';
const SERVER_NAME = 'excalibase';

export function mcpUrl(origin: string, projectId: string, readOnly: boolean): string {
  const url = `${origin.replace(/\/+$/, '')}/mcp?project=${encodeURIComponent(projectId)}`;
  return readOnly ? `${url}&read_only=true` : url;
}

function exportToken(token: string): SetupStep {
  return { label: 'Put the token in your shell environment (it is shown once)', code: `export ${TOKEN_ENV}=${token}` };
}

function jsonConfig(urlKey: 'url' | 'httpUrl', url: string, authorization: string): string {
  const server = { [urlKey]: url, headers: { Authorization: authorization } };
  return JSON.stringify({ mcpServers: { [SERVER_NAME]: server } }, null, 2);
}

export function mcpSetup(tool: AiToolId, url: string, token: string): SetupStep[] {
  switch (tool) {
    case 'claude-code':
      return [
        exportToken(token),
        {
          label: 'Add the server',
          code: `claude mcp add --transport http ${SERVER_NAME} "${url}" --header "Authorization: Bearer $${TOKEN_ENV}"`,
        },
      ];
    case 'cursor':
      return [
        exportToken(token),
        { label: 'Add to the project', file: '.cursor/mcp.json', code: jsonConfig('url', url, `Bearer \${env:${TOKEN_ENV}}`) },
      ];
    case 'codex':
      return [
        exportToken(token),
        {
          label: 'Add to the Codex config',
          file: '~/.codex/config.toml',
          code: `[mcp_servers.${SERVER_NAME}]\nurl = "${url}"\nbearer_token_env_var = "${TOKEN_ENV}"`,
        },
      ];
    case 'gemini':
      return [
        exportToken(token),
        { label: 'Add to the project', file: '.gemini/settings.json', code: jsonConfig('httpUrl', url, `Bearer $${TOKEN_ENV}`) },
      ];
  }
}
