import { describe, expect, it } from 'vitest';
import { nextIndex } from './tabs';

describe('nextIndex', () => {
  it.each([
    [0, 'ArrowRight', 8, 1],
    [7, 'ArrowRight', 8, 0],
    [3, 'ArrowLeft', 8, 2],
    [0, 'ArrowLeft', 8, 7],
    [4, 'Home', 8, 0],
    [4, 'End', 8, 7],
    [0, 'ArrowRight', 1, 0],
    [0, 'ArrowLeft', 1, 0],
  ])('from %i with %s of %i tabs goes to %i', (current, key, length, want) => {
    expect(nextIndex(current, key, length)).toBe(want);
  });

  it.each(['Tab', 'Enter', ' ', 'ArrowUp', 'ArrowDown', 'a'])('leaves %s alone', (key) => {
    expect(nextIndex(2, key, 8)).toBeNull();
  });

  it('does nothing for an empty tab list', () => {
    expect(nextIndex(0, 'ArrowRight', 0)).toBeNull();
  });
});
