import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, expect, test, vi } from 'vitest';
import type { FileReadResult } from '../api/types';
import { FilePreviewSheet } from './FilePreviewSheet';

afterEach(cleanup);

const payload = {
  preview: { path: 'go.mod', size: 8, content: 'module x' } as FileReadResult,
  fullPath: '/root/project/go.mod',
  displayPath: 'go.mod',
};

test('renders preview content + display path when open with payload', () => {
  render(<FilePreviewSheet open={true} onOpenChange={vi.fn()} payload={payload} />);
  expect(screen.getByText('module x')).toBeInTheDocument();
  expect(screen.getByText('go.mod')).toBeInTheDocument();
});

test('Copy path writes fullPath, not displayPath, to clipboard', async () => {
  const writeText = vi.fn().mockResolvedValue(undefined);
  Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
  render(<FilePreviewSheet open={true} onOpenChange={vi.fn()} payload={payload} />);
  fireEvent.click(screen.getByRole('button', { name: /Copy path/i }));
  expect(writeText).toHaveBeenCalledWith('/root/project/go.mod');
});

test('close button invokes onOpenChange(false)', () => {
  const onOpenChange = vi.fn();
  render(<FilePreviewSheet open={true} onOpenChange={onOpenChange} payload={payload} />);
  fireEvent.click(screen.getByRole('button', { name: /关闭预览/ }));
  expect(onOpenChange).toHaveBeenCalledWith(false);
});

test('renders nothing visible when open=false', () => {
  render(<FilePreviewSheet open={false} onOpenChange={vi.fn()} payload={payload} />);
  expect(screen.queryByText('module x')).not.toBeInTheDocument();
});
