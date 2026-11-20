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
      // Every source file is measured, tested or not: a file absent from lcov
      // is read by Sonar as uncovered, so a hand-kept allowlist let new files
      // fail the gate at 0% until someone remembered to add them. main.tsx and
      // src/test/** are excluded here and in sonar.coverage.exclusions alike.
      include: ['src/**/*.{ts,tsx}'],
      exclude: ['**/*.d.ts', '**/__tests__/**', '**/*.test.{ts,tsx}', 'src/test/**', 'src/main.tsx'],
      thresholds: {
        lines: 80,
        statements: 80,
        functions: 80,
        branches: 75,
      },
    },
  },
});
