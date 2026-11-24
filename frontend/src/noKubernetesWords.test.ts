import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import ts from 'typescript';
import { describe, expect, test } from 'vitest';

// EXC-850: a customer never sees Kubernetes words in Studio. This reads every
// string literal and JSX text node of every non-admin source file, which is
// what can reach the screen, and fails on the words below.
const FORBIDDEN = /\b(kubernetes|k8s|pods?|cnpg|cloudnativepg|namespaces?|operators?|nodes?|clusters?)\b/i;

// Admin-only surfaces may keep the technical words.
const ADMIN_FILE = /admin/i;

// Each entry is a literal that matches the pattern but is not a Kubernetes
// word shown to a customer. Keep it minimal and give every entry a reason.
const ALLOWED: ReadonlyArray<{ file: string; includes: string; reason: string }> = [
  { file: 'api/clusterSettings.ts', includes: 'cluster', reason: 'a query key and an API URL path, never rendered' },
  { file: 'components/MySQLConnection.tsx', includes: 'node', reason: 'Node.js in a code sample and its test id' },
  { file: 'components/sites/SiteBuildSettingsForm.tsx', includes: 'Node', reason: 'the Node.js runtime a site builds with' },
  { file: 'components/sites/siteCiSnippets.ts', includes: 'node', reason: 'Node.js in the CI snippets' },
  { file: 'pages/BackupsPage.tsx', includes: 'hibernated cluster', reason: 'matches the failure text the server records, not shown as is' },
  { file: 'pages/ProvisionPage.tsx', includes: 'k8s', reason: 'internal deploy-mode value, never rendered' },
  { file: 'utils/permissionRules.ts', includes: 'nodes', reason: 'nodes of a permission expression tree' },
  { file: 'utils/permissionRules.ts', includes: 'comparison operator', reason: 'a comparison operator in a permission rule' },
];

const SRC = join(__dirname);

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return name === 'test' ? [] : sourceFiles(path);
    const isSource = /\.(ts|tsx)$/.test(name) && !/\.(test|spec)\.(ts|tsx)$/.test(name) && !name.endsWith('.d.ts');
    return isSource && !ADMIN_FILE.test(relative(SRC, path)) ? [path] : [];
  });
}

function textsOf(file: string): string[] {
  const source = ts.createSourceFile(file, readFileSync(file, 'utf8'), ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX);
  const texts: string[] = [];
  const visit = (node: ts.Node) => {
    if (ts.isImportDeclaration(node) || ts.isExportDeclaration(node) || ts.isTypeNode(node)) return;
    if (ts.isJsxAttribute(node) && /^(className|data-testid|key|id|htmlFor)$/.test(node.name.getText())) return;
    if (
      ts.isStringLiteral(node) ||
      ts.isNoSubstitutionTemplateLiteral(node) ||
      ts.isTemplateHead(node) ||
      ts.isTemplateMiddle(node) ||
      ts.isTemplateTail(node) ||
      ts.isJsxText(node)
    ) {
      texts.push(node.text);
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return texts;
}

describe('customer-facing text', () => {
  test('names no Kubernetes words outside the admin pages', () => {
    const offenders = sourceFiles(SRC).flatMap((file) => {
      const name = relative(SRC, file);
      return textsOf(file)
        .filter((text) => FORBIDDEN.test(text))
        .filter((text) => !ALLOWED.some((a) => a.file === name && text.toLowerCase().includes(a.includes.toLowerCase())))
        .map((text) => `${name}: ${text.trim().slice(0, 100)}`);
    });
    expect(offenders).toEqual([]);
  });
});
