import { describe, test, expect, vi, beforeEach } from 'vitest';
import { screen, within } from '@testing-library/react';
import { HomePage } from './HomePage';
import { creditOverview, mountWith, project } from '../../test/homeFixtures';

vi.mock('../../api/client', () => ({ api: { get: vi.fn(), post: vi.fn(), delete: vi.fn() } }));

beforeEach(() => vi.clearAllMocks());
const widget = async () => within(await screen.findByRole('region', { name: 'Credits & usage' }));

describe('Home credits widget', () => {
  test('shows the balance and how long it lasts', async () => {
    mountWith({ projects: [project('p1')], features: { credits: true }, credits: creditOverview() }, <HomePage />);
    const credits = await widget();
    expect(await credits.findByText('$300.00')).toBeInTheDocument();
    expect(credits.getByText('Credit lasts another')).toBeInTheDocument();
    expect(credits.queryByRole('status')).not.toBeInTheDocument();
  });

  test('warns when the credit runs low and when it is used up', async () => {
    mountWith({ projects: [project('p1')], features: { credits: true }, credits: creditOverview({ balanceMicros: 30_000_000 }) }, <HomePage />);
    expect(await (await widget()).findByText('Your credit is running low.')).toBeInTheDocument();
  });

  test('says used up at zero', async () => {
    mountWith({ projects: [project('p1')], features: { credits: true }, credits: creditOverview({ balanceMicros: 0, lots: [] }) }, <HomePage />);
    expect(await (await widget()).findByText('Your credit is used up.')).toBeInTheDocument();
  });

  test('shows no money while the feature is off', async () => {
    mountWith({ projects: [project('p1')], credits: creditOverview() }, <HomePage />);
    const credits = await widget();
    expect(await credits.findByText('Projects')).toBeInTheDocument();
    expect(credits.queryByText('Credit balance')).not.toBeInTheDocument();
  });
});
