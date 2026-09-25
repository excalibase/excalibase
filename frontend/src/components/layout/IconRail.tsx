import { useState, useEffect } from 'react';
import { useLocation, useParams, useNavigate, Link } from 'react-router-dom';
import { visibleProjectNav, type NavSection } from './navigation';
import { useAppHostingEnabled } from '../../hooks/useDeploymentMode';
import { cn } from '../../utils/cn';
import { PanelLeftDashed } from 'lucide-react';

type SidebarMode = 'expanded' | 'collapsed' | 'expandable';

const STORAGE_KEY = 'sidebar-behavior';

interface IconRailProps {
  readonly activeSection: string | null;
  readonly onSectionClick: (key: string) => void;
}

export function IconRail({ activeSection, onSectionClick }: IconRailProps) {
  const { projectId } = useParams<{ projectId: string }>();
  const navigate = useNavigate();
  const location = useLocation();
  const basePath = `/project/${projectId}`;
  const { enabled: appHosting } = useAppHostingEnabled();
  const sections = visibleProjectNav({ appHosting });

  const [mode, setMode] = useState<SidebarMode>(() => {
    return (localStorage.getItem(STORAGE_KEY) as SidebarMode) ?? 'expandable';
  });
  const [hovered, setHovered] = useState(false);
  const [showModeMenu, setShowModeMenu] = useState(false);

  useEffect(() => {
    localStorage.setItem(STORAGE_KEY, mode);
  }, [mode]);

  const isExpanded = mode === 'expanded' || (mode === 'expandable' && hovered);

  const handleClick = (section: NavSection) => {
    if (section.children) {
      onSectionClick(section.key === activeSection ? '' : section.key);
      if (section.key !== activeSection) {
        navigate(`${basePath}/${section.children[0].to}`);
      }
    } else if (section.to !== undefined) {
      onSectionClick('');
      navigate(`${basePath}/${section.to}`);
    }
  };

  const isActive = (section: NavSection): boolean => {
    const path = location.pathname;
    if (section.to !== undefined) {
      const fullPath = `${basePath}/${section.to}`;
      return section.to === '' ? path === basePath || path === basePath + '/' : path.startsWith(fullPath);
    }
    if (section.children) {
      return section.children.some((c) => path.includes(`/${c.to.split('/')[0]}/`) || path.endsWith(`/${c.to}`));
    }
    return false;
  };

  return (
    <aside
      aria-label="Project navigation rail"
      className={cn(
        'flex-shrink-0 flex flex-col bg-surface-card border-r border-border-primary transition-all duration-200',
        isExpanded ? 'w-48' : 'w-12'
      )}
      onMouseEnter={() => setHovered(true)}
      onMouseLeave={() => { setHovered(false); setShowModeMenu(false); }}
      onFocus={() => setHovered(true)}
      onBlur={() => { setHovered(false); setShowModeMenu(false); }}
      data-testid="icon-rail"
    >
      {/* Back to platform */}
      <Link
        to="/"
        className={cn(
          'flex items-center gap-2.5 px-3 py-3 border-b border-border-primary hover:bg-surface-hover transition-colors',
          isExpanded ? 'justify-start' : 'justify-center'
        )}
        data-testid="back-to-platform"
      >
        <img src="/logo-icon.png" alt="" className="w-6 h-6 object-contain flex-shrink-0" />
        {isExpanded && <span className="text-sm font-bold text-text-primary truncate">Excalibase</span>}
      </Link>

      {/* Nav items */}
      <nav className="flex-1 flex flex-col py-2 gap-0.5 px-1.5 overflow-y-auto">
        {sections.map((section) => {
          const Icon = section.icon;
          const active = isActive(section) || activeSection === section.key;
          return (
            <button
              key={section.key}
              onClick={() => handleClick(section)}
              title={isExpanded ? undefined : section.label}
              data-testid={`nav-${section.key}`}
              className={cn(
                'flex items-center gap-2.5 rounded-lg transition-colors text-left',
                isExpanded ? 'px-3 py-2' : 'px-0 py-2 justify-center',
                active
                  ? 'bg-purple-500/15 text-purple-400'
                  : 'text-text-tertiary hover:text-text-primary hover:bg-surface-hover'
              )}
            >
              <Icon className="w-[18px] h-[18px] flex-shrink-0" />
              {isExpanded && <span className="text-[13px] font-medium truncate">{section.label}</span>}
            </button>
          );
        })}
      </nav>

      {/* Bottom: sidebar mode control */}
      <div className="relative px-1.5 py-2 border-t border-border-primary">
        <button
          onClick={() => setShowModeMenu(!showModeMenu)}
          title={isExpanded ? undefined : 'Sidebar settings'}
          data-testid="sidebar-settings"
          className={cn(
            'flex items-center gap-2.5 w-full rounded-lg text-text-tertiary hover:text-text-primary hover:bg-surface-hover transition-colors',
            isExpanded ? 'px-3 py-2' : 'px-0 py-2 justify-center'
          )}
        >
          <PanelLeftDashed className="w-[18px] h-[18px] flex-shrink-0" />
          {isExpanded && <span className="text-[13px] font-medium">Sidebar</span>}
        </button>

        {showModeMenu && (
          <div className="absolute bottom-full left-1 mb-1 w-48 bg-surface-card border border-border-primary rounded-xl shadow-lg z-50 py-1">
            <p className="px-3 py-1.5 text-[10px] font-semibold uppercase tracking-wider text-text-tertiary">Sidebar control</p>
            {([
              ['expanded', 'Expanded'],
              ['collapsed', 'Collapsed'],
              ['expandable', 'Expand on hover'],
            ] as const).map(([value, label]) => (
              <button
                key={value}
                onClick={() => { setMode(value); setShowModeMenu(false); }}
                className={cn(
                  'flex items-center gap-2 w-full px-3 py-1.5 text-xs transition-colors',
                  mode === value ? 'text-purple-400 bg-purple-500/10' : 'text-text-secondary hover:bg-surface-hover hover:text-text-primary'
                )}
              >
                <span className={cn('w-2 h-2 rounded-full', mode === value ? 'bg-purple-400' : 'bg-transparent')} />
                {label}
              </button>
            ))}
          </div>
        )}
      </div>
    </aside>
  );
}
