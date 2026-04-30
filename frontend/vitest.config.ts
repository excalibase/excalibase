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
      reporter: ['text', 'html', 'json-summary'],
      // Coverage gate covers only the files this branch added or modified
      // heavily — the rest of the studio (older hooks/pages without unit
      // tests) is tracked separately and improved incrementally.
      include: [
        'src/hooks/useRealtime.ts',
        'src/hooks/useSetup.ts',
        'src/hooks/useVault.ts',
        'src/components/auth/VaultGuard.tsx',
        'src/components/auth/AuthGuard.tsx',
        'src/pages/SetupPage.tsx',
        'src/pages/RealtimePage.tsx',
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
