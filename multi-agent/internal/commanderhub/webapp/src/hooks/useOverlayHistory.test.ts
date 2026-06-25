import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { createOverlayController } from './useOverlayHistory';

beforeEach(() => {
  // Reset history state between tests.
  window.history.replaceState(null, '', window.location.pathname);
});

afterEach(() => {
  vi.restoreAllMocks();
});

test('open(id) pushes a history entry tagged with commanderOverlay', () => {
  const controller = createOverlayController();
  controller.open('sessions');
  expect(window.history.state).toEqual({ commanderOverlay: 'sessions' });
  expect(controller.stackSnapshot()).toEqual(['sessions']);
});

test('closeTop calls history.back when id matches the top', () => {
  const controller = createOverlayController();
  const back = vi.spyOn(window.history, 'back').mockImplementation(() => {});
  controller.open('files');
  controller.closeTop('files');
  expect(back).toHaveBeenCalledTimes(1);
});

test('closeTop is a no-op when id does not match the top', () => {
  const controller = createOverlayController();
  const back = vi.spyOn(window.history, 'back').mockImplementation(() => {});
  controller.open('files');
  controller.closeTop('sessions');
  expect(back).not.toHaveBeenCalled();
});

test('popstate pops the stack and notifies onPop subscribers', () => {
  const controller = createOverlayController();
  const handler = vi.fn();
  controller.onPop(handler);
  controller.open('files');
  controller.open('preview');
  window.dispatchEvent(new PopStateEvent('popstate', { state: { commanderOverlay: 'files' } }));
  expect(handler).toHaveBeenCalledWith('preview');
  expect(controller.stackSnapshot()).toEqual(['files']);
});

test('popstate with empty stack is ignored', () => {
  const controller = createOverlayController();
  const handler = vi.fn();
  controller.onPop(handler);
  window.dispatchEvent(new PopStateEvent('popstate', { state: null }));
  expect(handler).not.toHaveBeenCalled();
});

test('reset detaches listener and never touches history', () => {
  const controller = createOverlayController();
  const handler = vi.fn();
  controller.onPop(handler);
  const back = vi.spyOn(window.history, 'back').mockImplementation(() => {});
  controller.open('sessions');
  controller.reset();
  window.dispatchEvent(new PopStateEvent('popstate', { state: null }));
  expect(handler).not.toHaveBeenCalled();
  expect(back).not.toHaveBeenCalled();
  expect(controller.stackSnapshot()).toEqual([]);
});

test('drainForBreakpoint calls history.go(-len) once and clears the stack', () => {
  const controller = createOverlayController();
  const go = vi.spyOn(window.history, 'go').mockImplementation(() => {});
  controller.open('files');
  controller.open('preview');
  controller.drainForBreakpoint();
  expect(go).toHaveBeenCalledExactlyOnceWith(-2);
  expect(controller.stackSnapshot()).toEqual([]);
});

test('drainForBreakpoint with empty stack does not call history.go', () => {
  const controller = createOverlayController();
  const go = vi.spyOn(window.history, 'go').mockImplementation(() => {});
  controller.drainForBreakpoint();
  expect(go).not.toHaveBeenCalled();
});
