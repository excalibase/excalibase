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
      include: [
        'src/hooks/**/*.{ts,tsx}',
        'src/components/auth/**/*.{ts,tsx}',
        'src/pages/SetupPage.tsx',
        'src/pages/RealtimePage.tsx',
        'src/realtime/**/*.{ts,tsx}',
        'src/components/RealtimeIndicator.tsx',
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
