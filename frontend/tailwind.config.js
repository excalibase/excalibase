/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      keyframes: {
        'slide-in-right': {
          '0%': { transform: 'translateX(100%)' },
          '100%': { transform: 'translateX(0)' },
        },
      },
      animation: {
        'slide-in-right': 'slide-in-right 0.2s ease-out',
      },
      colors: {
        // Layout tokens — hex CSS vars, switch on :root / .dark
        'bg-primary':       'var(--bg-primary)',
        'bg-secondary':     'var(--bg-secondary)',
        'bg-tertiary':      'var(--bg-tertiary)',
        'surface-card':     'var(--surface-card)',
        'surface-hover':    'var(--surface-hover)',
        'border-primary':   'var(--border-primary)',
        'border-secondary': 'var(--border-secondary)',
        'text-primary':     'var(--text-primary)',
        'text-secondary':   'var(--text-secondary)',
        'text-tertiary':    'var(--text-tertiary)',
        // Accent — RGB channels so /10 /30 opacity modifiers work
        'accent-primary':       'rgb(var(--accent-primary)       / <alpha-value>)',
        'accent-primary-hover': 'rgb(var(--accent-primary-hover) / <alpha-value>)',
        // Semantic — fixed
        'color-success': '#22c55e',
        'color-warning': '#f59e0b',
        'color-error':   '#ef4444',
      },
    },
  },
  plugins: [],
}
