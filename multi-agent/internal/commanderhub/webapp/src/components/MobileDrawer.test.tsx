import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import { MobileDrawer } from './MobileDrawer';

afterEach(cleanup);

test('renders children when open=true and hides them when open=false', () => {
  const { rerender } = render(
    <MobileDrawer open={false} onOpenChange={vi.fn()} side="left" title="Sessions">
      <p>inside</p>
    </MobileDrawer>,
  );
  expect(screen.queryByText('inside')).not.toBeInTheDocument();
  rerender(
    <MobileDrawer open={true} onOpenChange={vi.fn()} side="left" title="Sessions">
      <p>inside</p>
    </MobileDrawer>,
  );
  expect(screen.getByText('inside')).toBeInTheDocument();
  expect(screen.getByRole('dialog')).toHaveAttribute('aria-modal', 'true');
});

test('clicking the close button invokes onOpenChange(false)', () => {
  const onOpenChange = vi.fn();
  render(
    <MobileDrawer open={true} onOpenChange={onOpenChange} side="right" title="Files">
      <p>inside</p>
    </MobileDrawer>,
  );
  fireEvent.click(screen.getByRole('button', { name: /关闭 Files/ }));
  expect(onOpenChange).toHaveBeenCalledWith(false);
});

test('pressing ESC invokes onOpenChange(false) (Radix wires this)', () => {
  const onOpenChange = vi.fn();
  render(
    <MobileDrawer open={true} onOpenChange={onOpenChange} side="left" title="Sessions">
      <p>inside</p>
    </MobileDrawer>,
  );
  fireEvent.keyDown(document.activeElement || document.body, { key: 'Escape' });
  expect(onOpenChange).toHaveBeenCalledWith(false);
});

test('content carries side-specific testid and class', () => {
  render(
    <MobileDrawer open={true} onOpenChange={vi.fn()} side="right" title="Files">
      <p>inside</p>
    </MobileDrawer>,
  );
  const content = screen.getByTestId('drawer-right');
  expect(content.classList.contains('mobile-drawer-right')).toBe(true);
});
