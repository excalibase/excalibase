import { Outlet } from 'react-router-dom';
import { Database, Sun, Moon } from 'lucide-react';
import { useDarkMode } from '../../hooks/useDarkMode';

export function AuthLayout() {
  const { dark, toggle } = useDarkMode();

  return (
    <div className="min-h-screen bg-bg-primary flex items-center justify-center p-4 relative">
      {/* Theme toggle */}
      <button
        onClick={toggle}
        className="absolute top-4 right-4 p-2 rounded-lg text-text-secondary hover:text-text-primary hover:bg-surface-hover transition-colors"
      >
        {dark ? <Sun className="w-5 h-5" /> : <Moon className="w-5 h-5" />}
      </button>

      <div className="w-full max-w-md">
        {/* Branding */}
        <div className="flex flex-col items-center mb-8">
          <div className="w-14 h-14 rounded-2xl bg-purple-500/10 border border-purple-500/30 flex items-center justify-center mb-4">
            <Database className="w-7 h-7 text-purple-400" />
          </div>
          <h1 className="text-2xl font-bold text-text-primary">Excalibase</h1>
          <p className="text-sm text-text-secondary mt-1">Database provisioning platform</p>
        </div>

        {/* Card */}
        <div className="bg-surface-card border border-border-primary rounded-xl p-6 shadow-lg">
          <Outlet />
        </div>
      </div>
    </div>
  );
}
