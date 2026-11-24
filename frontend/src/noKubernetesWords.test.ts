import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import ts from 'typescript';
import { describe, expect, test } from 'vitest';

// EXC-850: a customer never sees Kubernetes words in Studio. This reads every
// string literal and JSX text node of every non-admin source file, which is
// what can reach the screen, and fails on the words below.
const FORBIDDEN = /\b(kubernetes|k8s|pods?|cnpg|cloudnativepg|namespaces?|operators?|nodes?|clusters?|crds?|kubectl|helm|pvcs?)\b/i;

// Admin-only surfaces may keep the technical words.
const ADMIN_PATH = /^(components\/admin\/|pages\/PlatformAdminPage\.tsx$|hooks\/useAdmin[^/]*$|api\/adminConsole\.ts$)/;

// Exact literals (trimmed, whole string) that match the pattern but are not a
// Kubernetes word shown to a customer. Keep it minimal; give every entry a reason.
const ALLOWED_LITERALS: ReadonlyArray<{ file: string; literal: string; reason: string }> = [
  { file: 'api/clusterSettings.ts', literal: 'cluster-settings', reason: 'a query key, never rendered' },
  { file: 'api/clusterSettings.ts', literal: '/cluster', reason: 'an API URL path, never rendered' },
  { file: 'components/MySQLConnection.tsx', literal: 'conn-mysql-node', reason: 'a test id' },
  { file: 'components/MySQLConnection.tsx', literal: 'Node (mysql2)', reason: 'the Node.js runtime in a snippet label' },
  { file: 'components/sites/SiteBuildSettingsForm.tsx', literal: 'Node version', reason: 'the Node.js runtime a site builds with' },
  { file: 'components/sites/SiteBuildSettingsForm.tsx', literal: 'The Node major the build runs on.', reason: 'the Node.js runtime a site builds with' },
  { file: 'components/sites/SiteBuildSettingsForm.tsx', literal: 'Node', reason: 'the Node.js runtime option' },
  { file: 'utils/backupView.ts', literal: 'cannot backup a hibernated cluster', reason: 'matched against the failure text the server records, not shown as is' },
  { file: 'pages/ProvisionPage.tsx', literal: 'k8s', reason: 'internal deploy-mode value, never rendered' },
  { file: 'utils/permissionRules.ts', literal: 'nodes', reason: 'nodes of a permission expression tree' },
  { file: 'utils/permissionRules.ts', literal: '" is not a comparison operator', reason: 'a comparison operator in a permission rule' },
];

// Multi-line code samples: only the listed word is ignored in that file, so
// any other forbidden word in the same literal still fails.
const SAMPLE_WORDS: ReadonlyArray<{ file: string; word: string; reason: string }> = [
  { file: 'components/MySQLConnection.tsx', word: 'node', reason: "'node:fs' in a Node.js code sample" },
  { file: 'components/sites/siteCiSnippets.ts', word: 'node', reason: 'Node.js in the CI snippets (setup-node, node:24)' },
];

export function offendersIn(name: string, texts: readonly string[]): string[] {
  return texts
    .filter((text) => !ALLOWED_LITERALS.some((a) => a.file === name && a.literal === text.trim()))
    .filter((text) => {
      const ignored = SAMPLE_WORDS.filter((w) => w.file === name).map((w) => new RegExp(`\\b${w.word}\\b`, 'gi'));
      return FORBIDDEN.test(ignored.reduce((rest, re) => rest.replace(re, ''), text));
    })
    .map((text) => `${name}: ${text.trim().slice(0, 100)}`);
}

const SRC = join(__dirname);

function sourceFiles(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return name === 'test' ? [] : sourceFiles(path);
    const isSource = /\.(ts|tsx)$/.test(name) && !/\.(test|spec)\.(ts|tsx)$/.test(name) && !name.endsWith('.d.ts');
    return isSource && !ADMIN_PATH.test(relative(SRC, path)) ? [path] : [];
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
    const offenders = sourceFiles(SRC).flatMap((file) => offendersIn(relative(SRC, file), textsOf(file)));
    expect(offenders).toEqual([]);
  });

  test('an allowlisted literal does not excuse a longer string that contains it', () => {
    expect(offendersIn('pages/ProvisionPage.tsx', ['k8s'])).toEqual([]);
    expect(offendersIn('pages/ProvisionPage.tsx', ['k8s pods in your cluster'])).toHaveLength(1);
    expect(offendersIn('components/sites/siteCiSnippets.ts', ['image: node:24'])).toEqual([]);
    expect(offendersIn('components/sites/siteCiSnippets.ts', ['image: node:24 on a pod'])).toHaveLength(1);
  });
});
