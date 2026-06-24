import type { ReactNode } from 'react';
import * as Dialog from '@radix-ui/react-dialog';
import { X } from 'lucide-react';

export function MobileDrawer({
  open,
  onOpenChange,
  side,
  title,
  children,
}: {
  open: boolean;
  onOpenChange: (next: boolean) => void;
  side: 'left' | 'right';
  title: string;
  children: ReactNode;
}) {
  return (
    <Dialog.Root open={open} onOpenChange={onOpenChange}>
      <Dialog.Portal>
        <Dialog.Overlay className="mobile-overlay" />
        <Dialog.Content
          className={`mobile-drawer mobile-drawer-${side}`}
          data-testid={`drawer-${side}`}
          aria-modal="true"
          aria-describedby={undefined}
        >
          <header className="mobile-drawer-header">
            <Dialog.Title className="mobile-drawer-title">{title}</Dialog.Title>
            <Dialog.Close asChild>
              <button
                className="mobile-drawer-close"
                type="button"
                aria-label={`关闭 ${title}`}
              >
                <X size={20} />
              </button>
            </Dialog.Close>
          </header>
          <div className="mobile-drawer-body">{children}</div>
        </Dialog.Content>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
