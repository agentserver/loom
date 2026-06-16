import { render, screen } from '@testing-library/react';
import { expect, test } from 'vitest';
import { FilePreview } from './FileExplorerPanel';

test('shows too-large file metadata instead of content', () => {
  render(<FilePreview preview={{ path: 'large.log', size: 3_000_000, too_large: true }} />);
  expect(screen.getByText('large.log')).toBeInTheDocument();
  expect(screen.getByText(/2MB/)).toBeInTheDocument();
});
