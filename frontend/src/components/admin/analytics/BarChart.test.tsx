import { describe, expect, test } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { BarChart, chartScale, tooltipShift } from './BarChart';

const labels = ['11-25', '11-26', '11-27'];

describe('chartScale', () => {
  test('rounds the top up to a whole step and never collapses to zero', () => {
    expect(chartScale(0)).toEqual({ top: 5, step: 5 });
    expect(chartScale(7)).toEqual({ top: 10, step: 5 });
    expect(chartScale(35)).toEqual({ top: 40, step: 10 });
    expect(chartScale(91)).toEqual({ top: 100, step: 20 });
  });
});

describe('tooltipShift', () => {
  test('is centred, except at the edges where it is pulled back inside the chart', () => {
    expect(tooltipShift(15, 30)).toBe('-translate-x-1/2');
    expect(tooltipShift(29, 30)).toBe('-translate-x-full');
    expect(tooltipShift(0, 30)).toBe('translate-x-0');
  });
});

describe('BarChart', () => {
  const chart = (series: number[][]) => (
    <BarChart label="Sign-ups per day" labels={labels} series={series} seriesClasses={['fill-accent-primary', 'fill-color-error']}
      tip={(i) => `${series[0][i] + (series[1]?.[i] ?? 0)} sign-ups`} />
  );

  test('draws a bar for every non-zero value and none for zero days', () => {
    const { container } = render(chart([[3, 0, 5], [1, 0, 0]]));
    expect(screen.getByRole('img', { name: 'Sign-ups per day' })).toBeInTheDocument();
    expect(container.querySelectorAll('rect[data-bar]')).toHaveLength(3);
  });

  test('hovering a day shows its label and the caller\'s text, leaving hides it', async () => {
    render(chart([[3, 0, 5], [1, 0, 0]]));
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
    await userEvent.hover(screen.getByTestId('bar-hit-2'));
    expect(screen.getByRole('tooltip')).toHaveTextContent('11-27 5 sign-ups');
    await userEvent.unhover(screen.getByTestId('bar-hit-2'));
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument();
  });

  test('an all-zero series still draws the axes and no bars', () => {
    const { container } = render(chart([[0, 0, 0]]));
    expect(container.querySelectorAll('rect[data-bar]')).toHaveLength(0);
    expect(container.querySelectorAll('line').length).toBeGreaterThan(1);
  });
});
