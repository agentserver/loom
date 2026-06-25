import { act, renderHook } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import { useMediaQuery } from './useMediaQuery';

type MQLListener = (ev: MediaQueryListEvent) => void;

function installMatchMedia(initialMatches: boolean) {
  const listeners = new Set<MQLListener>();
  let matches = initialMatches;
  const mql = {
    get matches() {
      return matches;
    },
    media: '',
    addEventListener: (_: 'change', l: MQLListener) => listeners.add(l),
    removeEventListener: (_: 'change', l: MQLListener) => listeners.delete(l),
    dispatchEvent: () => true,
    onchange: null,
  } as unknown as MediaQueryList;
  window.matchMedia = vi.fn().mockReturnValue(mql);
  return {
    flip(next: boolean) {
      matches = next;
      for (const l of listeners) l({ matches } as MediaQueryListEvent);
    },
  };
}

afterEach(() => {
  // restore matchMedia between tests
  // @ts-expect-error allow reset
  delete window.matchMedia;
});

test('returns the initial match value', () => {
  installMatchMedia(true);
  const { result } = renderHook(() => useMediaQuery('(max-width: 1023px)'));
  expect(result.current).toBe(true);
});

test('re-renders when the media query flips', () => {
  const ctrl = installMatchMedia(false);
  const { result } = renderHook(() => useMediaQuery('(max-width: 1023px)'));
  expect(result.current).toBe(false);
  act(() => ctrl.flip(true));
  expect(result.current).toBe(true);
});

test('invokes onChange synchronously before the new render', () => {
  const ctrl = installMatchMedia(true);
  const order: string[] = [];
  const onChange = vi.fn((next: boolean) => {
    // At the moment onChange runs, the hook has not yet called setMatches,
    // so any side-effect (history.go, state reset) runs before React commits.
    order.push(`onChange:${next}`);
  });
  const { result } = renderHook(() => useMediaQuery('(max-width: 1023px)', { onChange }));
  order.push(`initial:${result.current}`);
  act(() => ctrl.flip(false));
  order.push(`after:${result.current}`);
  expect(onChange).toHaveBeenCalledWith(false);
  // The synchronous-before-commit ordering: onChange precedes the post-flip
  // render observation.
  expect(order).toEqual(['initial:true', 'onChange:false', 'after:false']);
});
