import { describe, test, expect, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { StageTimeline } from './StageTimeline';
import { PipelineVisualizer } from '../PipelineVisualizer';
import { STAGE_LABELS } from '../../utils/stageLabels';
import { ProvisioningStage } from '../../types';
import { useAuthStore } from '../../stores/auth-store';

const VIEWS = [
  ['StageTimeline', StageTimeline],
  ['PipelineVisualizer', PipelineVisualizer],
] as const;

describe.each(VIEWS)('%s', (_name, View) => {
  beforeEach(() => {
    useAuthStore.setState({ user: { id: 'u1', username: 'u', email: 'u@e.test', role: 'user' }, isAuthenticated: true });
  });

  test('labels its steps from the shared stage words', () => {
    render(<View currentStage={ProvisioningStage.CRD_DEPLOYMENT} />);
    for (const stage of [ProvisioningStage.VALIDATING, ProvisioningStage.CRD_DEPLOYMENT, ProvisioningStage.COMPLETED]) {
      expect(screen.getByText(STAGE_LABELS[stage])).toBeInTheDocument();
    }
  });

  test('a failure shows plain wording and not the raw text', () => {
    render(<View currentStage={ProvisioningStage.FAILED} failureReason="pod x ImagePullBackOff" />);
    expect(screen.getByText('Provisioning failed')).toBeInTheDocument();
    expect(screen.getByText(/image could not be downloaded/i)).toBeInTheDocument();
    expect(screen.queryByText(/ImagePullBackOff/)).not.toBeInTheDocument();
  });

  test('a failure without text still says so, in generic wording', () => {
    render(<View currentStage={ProvisioningStage.FAILED} />);
    expect(screen.getByText('Provisioning failed. Try again or contact support.')).toBeInTheDocument();
  });
});
