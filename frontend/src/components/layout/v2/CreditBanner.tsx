import { Link } from 'react-router-dom';
import { AlertTriangle } from 'lucide-react';
import { useOrgCredits } from '../../../api/credits';
import { useFeatureEnabled, useSupportEmail } from '../../../hooks/useDeploymentMode';
import { useCurrentOrg } from '../../../hooks/useMemberOrgs';
import { cn } from '../../../utils/cn';
import { creditNotice } from './creditNotice';

const TONE = {
  bad: 'border-red-500/30 bg-red-500/10 text-red-300',
  warn: 'border-amber-500/30 bg-amber-500/10 text-amber-300',
} as const;

// The console-wide notice about the current org's credit: used up (with the day compute pauses),
// expiring within a week, or running low. It reads the same credit the Usage page does and is
// absent while credits are off or the credit is fine.
export function CreditBanner() {
  const { org } = useCurrentOrg();
  const creditsOn = useFeatureEnabled('credits').enabled;
  const { data } = useOrgCredits(org?.id, creditsOn);
  const supportEmail = useSupportEmail();
  const notice = creditNotice(data, new Date(), supportEmail);
  if (!notice) return null;
  return (
    <output data-testid="credit-banner" className={cn('flex w-full flex-wrap items-center gap-x-3 gap-y-1 border-b px-4 py-2 text-sm', TONE[notice.tone])}>
      <AlertTriangle className="h-4 w-4 shrink-0" aria-hidden="true" />
      <span className="min-w-0 flex-1">{notice.text}</span>
      <Link to="/usage" className="font-medium underline">Usage &amp; credits</Link>
    </output>
  );
}
