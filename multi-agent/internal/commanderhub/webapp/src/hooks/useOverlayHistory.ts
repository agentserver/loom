import { useRef } from 'react';

export type OverlayID = 'sessions' | 'files' | 'preview';

export interface OverlayController {
  open(id: OverlayID): void;
  closeTop(id: OverlayID): void;
  reset(): void;
  drainForBreakpoint(): void;
  onPop(handler: (id: OverlayID) => void): () => void;
  stackSnapshot(): readonly OverlayID[];
}

export function createOverlayController(): OverlayController {
  const stack: OverlayID[] = [];
  const subscribers = new Set<(id: OverlayID) => void>();
  let listener: ((event: PopStateEvent) => void) | null = null;

  function ensureListener() {
    if (listener || typeof window === 'undefined') return;
    listener = () => {
      const popped = stack.pop();
      if (!popped) return;
      for (const handler of subscribers) handler(popped);
    };
    window.addEventListener('popstate', listener);
  }

  function detachListener() {
    if (!listener || typeof window === 'undefined') return;
    window.removeEventListener('popstate', listener);
    listener = null;
  }

  return {
    open(id) {
      ensureListener();
      stack.push(id);
      if (typeof window !== 'undefined') {
        window.history.pushState({ commanderOverlay: id }, '');
      }
    },
    closeTop(id) {
      if (stack[stack.length - 1] !== id) return;
      if (typeof window !== 'undefined') window.history.back();
    },
    reset() {
      detachListener();
      stack.length = 0;
      subscribers.clear();
    },
    drainForBreakpoint() {
      const len = stack.length;
      stack.length = 0;
      if (len > 0 && typeof window !== 'undefined') {
        window.history.go(-len);
      }
    },
    onPop(handler) {
      subscribers.add(handler);
      return () => subscribers.delete(handler);
    },
    stackSnapshot() {
      return stack.slice();
    },
  };
}

export function useOverlayHistory(): OverlayController {
  const ref = useRef<OverlayController | null>(null);
  if (ref.current === null) ref.current = createOverlayController();
  return ref.current;
}
