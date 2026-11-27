import { describe, test, expect, vi, beforeEach } from 'vitest';
import { screen, within } from '@testing-library/react';
import { PlanBudgetSection } from './PlanBudgetSection';
import { PlanBudgetLeft } from '../../components/PlanBudgetLeft';
import { HomePage } from '../home/HomePage';
import { api } from '../../api/client';
import { mountWith, planBudget, project } from '../../test/homeFixtures';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));
beforeEach(() => vi.clearAllMocks());

const on = { planbudget: true };

describe('Plan budget on the Usage page', () => {
  test('shows each dimension as used against the limit, and the services behind it', async () => {
    mountWith({ projects: [project('p1')], features: on, budget: planBudget() }, <PlanBudgetSection orgId="org-1" />);
    const section = within(await screen.findByRole('region', { name: 'Plan budget' }));
    expect(await section.findByText('0.5 vCPU of 2 vCPU')).toBeInTheDocument();
    expect(section.getByText('1 GiB of 4 GiB')).toBeInTheDocument();
    expect(section.getByText('5 GiB of 20 GiB')).toBeInTheDocument();
    expect(section.getByText('2 of 6')).toBeInTheDocument();
    expect(section.getByRole('meter', { name: 'CPU' })).toHaveAttribute('value', '25');
    expect(section.getByText('shop')).toBeInTheDocument();
    expect(section.getByText('web')).toBeInTheDocument();
  });

  test('says an org over its plan is over, never a negative left', async () => {
    mountWith({ projects: [project('p1')], features: on, budget: planBudget({ used: { cpuMilli: 2500, memoryBytes: 0, diskBytes: 0, services: 1 }, left: { cpuMilli: -500, memoryBytes: 0, diskBytes: 0, services: 5 } }) }, <PlanBudgetSection orgId="org-1" />);
    const section = within(await screen.findByRole('region', { name: 'Plan budget' }));
    expect(await section.findByText(/over your plan/i)).toBeInTheDocument();
    expect(section.queryByText(/-/)).toBeNull();
  });

  test('is absent while the feature is off, once the config has answered', async () => {
    mountWith({ projects: [project('p1')], features: {}, budget: planBudget() }, <><PlanBudgetSection orgId="org-1" /><p>ready</p></>);
    expect(await screen.findByText('ready')).toBeInTheDocument();
    await vi.waitFor(() => expect(api.get).toHaveBeenCalledWith('/config'));
    expect(screen.queryByRole('region', { name: 'Plan budget' })).toBeNull();
  });

  test('a read that fails says so and shows no numbers', async () => {
    mountWith({ projects: [project('p1')], features: on }, <PlanBudgetSection orgId="org-1" />);
    expect(await screen.findByRole('alert')).toHaveTextContent('Could not read your plan budget');
  });

  test('a flag that is on in config but empty from the server shows nothing', async () => {
    mountWith({ projects: [project('p1')], features: on, budget: planBudget({ enabled: false, services: [] }) }, <><PlanBudgetSection orgId="org-1" /><p>ready</p></>);
    expect(await screen.findByText('ready')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Plan budget' })).toBeNull();
  });
});

describe('what is left beside a create or resize', () => {
  test('names what is left in the plan', async () => {
    mountWith({ projects: [project('p1')], features: on, budget: planBudget() }, <PlanBudgetLeft orgId="org-1" />);
    expect(await screen.findByText(/Left in your plan: 1.5 vCPU, 3 GiB memory, 15 GiB disk/)).toBeInTheDocument();
  });

  test('says nothing without an org or while the feature is off', async () => {
    mountWith({ projects: [project('p1')], features: {}, budget: planBudget() }, <><PlanBudgetLeft orgId="org-1" /><PlanBudgetLeft orgId="" /><p>ready</p></>);
    expect(await screen.findByText('ready')).toBeInTheDocument();
    expect(screen.queryByText(/Left in your plan/)).toBeNull();
  });
});

describe('Home', () => {
  test('the usage widget gains the budget rows when the feature is on', async () => {
    mountWith({ projects: [project('p1')], features: { ...on, credits: false }, budget: planBudget() }, <HomePage />);
    const widget = within(await screen.findByRole('region', { name: 'Credits & usage' }));
    expect(await widget.findByText('0.5 vCPU of 2 vCPU')).toBeInTheDocument();
    expect(widget.getByText('1 GiB of 4 GiB')).toBeInTheDocument();
  });

  test('has no budget rows while the feature is off', async () => {
    mountWith({ projects: [project('p1')], features: {}, budget: planBudget() }, <HomePage />);
    const widget = within(await screen.findByRole('region', { name: 'Credits & usage' }));
    expect(await widget.findByText('Projects')).toBeInTheDocument();
    expect(widget.queryByText('0.5 vCPU of 2 vCPU')).toBeNull();
  });
});
