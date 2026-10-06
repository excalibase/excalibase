/// <reference types="node" />
import { readdirSync, readFileSync } from 'node:fs';
import { basename, join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

const SOURCE_ROOT = join(__dirname, '..');
const FORM_MARKUP = /<form\b|<input\b|<textarea\b|<select\b/;
const TEST_FILE = /\.test\.tsx?$/;

// Files that match the form markup but are not forms, or are tested elsewhere.
// Every entry needs a reason; never list a real form here to make this pass.
const ALLOWLIST: Record<string, string> = {};

function listFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const fullPath = join(directory, entry.name);
    return entry.isDirectory() ? listFiles(fullPath) : [fullPath];
  });
}

function escapeRegExp(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function findUncoveredForms(): string[] {
  const allFiles = listFiles(SOURCE_ROOT);
  const testText = allFiles
    .filter((path) => TEST_FILE.test(path))
    .map((path) => readFileSync(path, 'utf8'))
    .join('\n');
  return allFiles
    .filter((path) => path.endsWith('.tsx') && !TEST_FILE.test(path))
    .filter((path) => FORM_MARKUP.test(readFileSync(path, 'utf8')))
    .filter((path) => {
      const stem = basename(path, '.tsx');
      return !new RegExp(`from '[^']*/${escapeRegExp(stem)}'`).test(testText);
    })
    .map((path) => relative(SOURCE_ROOT, path).split('\\').join('/'))
    .filter((path) => !(path in ALLOWLIST))
    .sort();
}

describe('form coverage guard', () => {
  it('every component holding a form or input is imported by some Vitest file', () => {
    expect(findUncoveredForms()).toEqual([]);
  });

  it('allowlist entries each carry a reason', () => {
    for (const [path, reason] of Object.entries(ALLOWLIST)) {
      expect(reason.trim(), path).not.toBe('');
    }
  });
});
