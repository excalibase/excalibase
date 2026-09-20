// Rewrites the SF: paths in coverage/lcov.info from frontend-relative
// ("src/pages/TablesPage.tsx") to repo-relative ("frontend/src/pages/...").
//
// vitest writes them relative to the frontend/ package, but the Sonar scanner
// runs from the repository root and resolves report paths against ITS base
// directory. Paths it cannot resolve are dropped silently — the analysis
// succeeds and every .ts/.tsx file is scored 0% covered — so this runs as part
// of `npm run test:coverage` rather than only in CI, keeping a local scan
// (scripts/sonar-local.sh) honest too.
import { readFileSync, writeFileSync, existsSync } from 'node:fs';

const REPORT = new URL('../coverage/lcov.info', import.meta.url);
const PREFIX = 'frontend/';

if (!existsSync(REPORT)) {
  console.error(`lcov-repo-paths: ${REPORT.pathname} not found — did the coverage run fail?`);
  process.exit(1);
}

const rebased = readFileSync(REPORT, 'utf8').replaceAll(
  /^SF:(?!\/|[A-Za-z]:|frontend\/)/gm,
  `SF:${PREFIX}`,
);
writeFileSync(REPORT, rebased);

const count = (rebased.match(/^SF:frontend\//gm) ?? []).length;
console.log(`lcov-repo-paths: ${count} file(s) rebased onto ${PREFIX}`);
