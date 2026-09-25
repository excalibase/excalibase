import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';

export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    // Playwright e2e tests live under e2e/ — exclude them from the unit run.
    exclude: ['e2e/**', 'node_modules/**', 'dist/**'],
    coverage: {
      provider: 'v8',
      // lcov is what Sonar reads (sonar.javascript.lcov.reportPaths). Without
      // it every measured .ts/.tsx file reaches Sonar with no coverage data
      // at all and is scored 0%, however many tests actually cover it.
      reporter: ['text', 'html', 'json-summary', 'lcov'],
      // A file absent from this list is absent from lcov, and Sonar reads an
      // absent file as uncovered — so anything measured here must be listed.
      include: [
        'src/hooks/**/*.{ts,tsx}',
        'src/components/auth/**/*.{ts,tsx}',
        'src/pages/SetupPage.tsx',
        'src/pages/RealtimePage.tsx',
        'src/pages/TablesPage.tsx',
        'src/components/tables/ExposureToggle.tsx',
        'src/api/postgresCatalog.ts',
        'src/api/projectEndpoint.ts',
        'src/components/ConnectionStrings.tsx',
        'src/components/PostgresVersionPicker.tsx',
        'src/components/MinorUpgradeCard.tsx',
        'src/components/documents/**/*.{ts,tsx}',
        'src/pages/DocumentsPage.tsx',
        'src/api/documents.ts',
        'src/utils/mongoQuery.ts',
        'src/utils/extendedJson.ts',
        'src/components/layout/SubNav.tsx',
        'src/components/layout/navigation.ts',
        'src/components/InviteLinkNotice.tsx',
        'src/pages/RegisterPage.tsx',
        'src/pages/LoginPage.tsx',
        'src/pages/OrgDetailPage.tsx',
        'src/api/orgs.ts',
        'src/api/apps.ts',
        'src/components/containers/**/*.{ts,tsx}',
        'src/components/layout/IconRail.tsx',
        'src/pages/ContainersPage.tsx',
        'src/pages/ContainerFormPage.tsx',
        'src/pages/ContainerDetailPage.tsx',
      ],
      exclude: ['**/*.d.ts', '**/__tests__/**', '**/*.test.{ts,tsx}'],
      thresholds: {
        lines: 80,
        statements: 80,
        functions: 80,
        branches: 75,
      },
    },
  },
});
