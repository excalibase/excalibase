import { describe, test, expect } from 'vitest';
import { mcpUrl, mcpSetup, AI_TOOLS } from './mcpConfig';

const origin = 'https://app.example.test';

describe('MCP URL', () => {
  test('names the project and narrows to read-only when asked', () => {
    expect(mcpUrl(origin, 'proj-1', false)).toBe('https://app.example.test/mcp?project=proj-1');
    expect(mcpUrl(origin, 'proj-1', true)).toBe('https://app.example.test/mcp?project=proj-1&read_only=true');
  });

  test('escapes the project id', () => {
    expect(mcpUrl(origin, 'a b', false)).toBe('https://app.example.test/mcp?project=a%20b');
  });
});

describe('client setup', () => {
  const url = 'https://app.example.test/mcp?project=proj-1';
  const token = 'excb_secret';

  test('every supported client has a setup', () => {
    expect(AI_TOOLS.map((tool) => tool.id)).toEqual(['cursor', 'claude-code', 'codex', 'gemini']);
    for (const tool of AI_TOOLS) {
      const steps = mcpSetup(tool.id, url, token);
      expect(steps.length).toBeGreaterThan(0);
      expect(steps.map((step) => step.code).join('\n')).toContain(url);
    }
  });

  test('Claude Code adds an HTTP server with the bearer header', () => {
    const code = mcpSetup('claude-code', url, token).map((step) => step.code).join('\n');
    expect(code).toContain(`export EXCALIBASE_TOKEN=${token}`);
    expect(code).toContain(`claude mcp add --transport http excalibase "${url}" --header "Authorization: Bearer $EXCALIBASE_TOKEN"`);
  });

  test('Cursor gets .cursor/mcp.json with url and headers', () => {
    const step = mcpSetup('cursor', url, token).find((s) => s.file === '.cursor/mcp.json');
    const config = JSON.parse(step?.code ?? '{}');
    expect(config.mcpServers.excalibase.url).toBe(url);
    expect(config.mcpServers.excalibase.headers.Authorization).toBe('Bearer ${env:EXCALIBASE_TOKEN}');
  });

  test('Codex reads the token from an environment variable', () => {
    const step = mcpSetup('codex', url, token).find((s) => s.file === '~/.codex/config.toml');
    expect(step?.code).toBe(`[mcp_servers.excalibase]\nurl = "${url}"\nbearer_token_env_var = "EXCALIBASE_TOKEN"`);
  });

  test('Gemini CLI gets .gemini/settings.json with httpUrl and headers', () => {
    const step = mcpSetup('gemini', url, token).find((s) => s.file === '.gemini/settings.json');
    const config = JSON.parse(step?.code ?? '{}');
    expect(config.mcpServers.excalibase.httpUrl).toBe(url);
    expect(config.mcpServers.excalibase.headers.Authorization).toBe('Bearer $EXCALIBASE_TOKEN');
  });

  test('no config file holds the token itself', () => {
    for (const tool of AI_TOOLS) {
      for (const step of mcpSetup(tool.id, url, token)) {
        if (step.file) expect(step.code).not.toContain(token);
      }
    }
  });
});
