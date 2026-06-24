import * as Dialog from '@radix-ui/react-dialog';
import { Copy, X } from 'lucide-react';
import type { FileReadResult } from '../api/types';
import { FilePreview } from './FileExplorerPanel';

export type FilePreviewPayload = {
  preview: FileReadResult;
  fullPath: string;
  displayPath: string;
};

export function FilePreviewSheet({
  open,
  onOpenChange,
  payload,
}: {
  open: boolean;
  onOpenChange: (next: boolean) => void;
  payload: FilePreviewPayload | null;
}) {
  const fullPath = payload?.fullPath ?? '';
  const displayPath = payload?.displayPath ?? '';
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="mobile-overlay" />
        <Dialog.Content
          className="file-preview-sheet"
          data-testid="file-preview-sheet"
        >
          <header className="file-preview-sheet-header">
            <Dialog.Close asChild>
              <button
                type="button"
                className="file-preview-sheet-close"
                aria-label="关闭预览"
              >
                <X size={20} />
              </button>
            </Dialog.Close>
            <Dialog.Title asChild>
              <span className="file-preview-sheet-path" title={displayPath}>
                {displayPath}
              </span>
            </Dialog.Title>
            <button
              type="button"
              className="file-preview-sheet-copy"
              aria-label="Copy path"
              onClick={() => {
                if (fullPath) void navigator.clipboard?.writeText(fullPath);
              }}
            >
              <Copy size={16} /> Copy path
            </button>
          </header>
          <div className="file-preview-sheet-body">
            {payload ? <FilePreview preview={payload.preview} /> : null}
          </div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
