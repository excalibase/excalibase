import { describe, test, expect } from 'vitest';
import { budgetRefusal, cpuText, memoryText } from './planBudget';

describe('plan budget text', () => {
  test('CPU reads in vCPU', () => {
    expect(cpuText(1500)).toBe('1.5 vCPU');
    expect(cpuText(2000)).toBe('2 vCPU');
    expect(cpuText(250)).toBe('0.25 vCPU');
  });

  test('memory and disk read in MiB below a GiB and GiB above', () => {
    expect(memoryText(1024 ** 3)).toBe('1 GiB');
    expect(memoryText(1.5 * 1024 ** 3)).toBe('1.5 GiB');
    expect(memoryText(256 * 1024 ** 2)).toBe('256 MiB');
  });

  test('a 409 plan_budget_exceeded is recognised and carries the server text', () => {
    const error = { response: { status: 409, data: { code: 'plan_budget_exceeded', error: 'plan budget exceeded: needs 1 vCPU', dimension: 'CPU', left: { cpuMilli: 250 } } } };
    expect(budgetRefusal(error)).toMatchObject({ message: 'plan budget exceeded: needs 1 vCPU', dimension: 'CPU' });
  });

  test('a refusal says whether the credit being used up caused it', () => {
    const body = { code: 'plan_budget_exceeded', error: 'your credit is used up', dimension: 'CPU' };
    expect(budgetRefusal({ response: { status: 409, data: { ...body, creditExhausted: true } } })?.creditExhausted).toBe(true);
    expect(budgetRefusal({ response: { status: 409, data: body } })?.creditExhausted).toBe(false);
    expect(budgetRefusal({ response: { status: 409, data: { ...body, creditExhausted: 'yes' } } })?.creditExhausted).toBe(false);
    const budget = { cpuMilli: 1000, memoryBytes: 1000 };
    const fits = (need: unknown, ceiling: unknown = budget) => budgetRefusal({ response: { status: 409, data: { ...body, need, budget: ceiling } } })?.fitsCeilingAlone;
    expect(fits({ cpuMilli: 1000, memoryBytes: 1000 })).toBe(true);
    expect(fits({ cpuMilli: 1001, memoryBytes: 10 })).toBe(false);
    expect(fits({ cpuMilli: 10, memoryBytes: 1001 })).toBe(false);
    expect(fits(undefined)).toBe(false);
    expect(fits({ cpuMilli: 10, memoryBytes: 10 }, {})).toBe(false);
  });

  test('any other failure is not a budget refusal', () => {
    expect(budgetRefusal({ response: { status: 409, data: { code: 'other', error: 'x' } } })).toBeUndefined();
    expect(budgetRefusal({ response: { status: 500, data: {} } })).toBeUndefined();
    expect(budgetRefusal(new Error('boom'))).toBeUndefined();
  });
});
