import * as RadixDialog from '@radix-ui/react-dialog';
/** @jsxRuntime classic */
import React from 'react';
import { sxToStyle, token } from './style';

export function Dialog({
  open,
  onClose,
  children,
}: {
  open: boolean;
  onClose?: () => void;
  children: React.ReactNode;
  maxWidth?: string;
  fullWidth?: boolean;
}) {
  return (
    <RadixDialog.Root open={open} onOpenChange={nextOpen => !nextOpen && onClose?.()}>
      <RadixDialog.Portal>
        <RadixDialog.Overlay
          style={{
            background: 'rgba(15, 23, 42, 0.36)',
            inset: 0,
            position: 'fixed',
            zIndex: 40,
          }}
        />
        <RadixDialog.Content
          style={{
            background: token('surface'),
            border: `1px solid ${token('border')}`,
            borderRadius: 'var(--dns-ui-radius-md, 8px)',
            boxShadow: '0 24px 80px rgba(15, 23, 42, 0.24)',
            color: token('text'),
            left: '50%',
            maxHeight: 'calc(100vh - 64px)',
            maxWidth: 760,
            overflow: 'auto',
            position: 'fixed',
            top: '12vh',
            transform: 'translateX(-50%)',
            width: 'min(calc(100vw - 32px), 760px)',
            zIndex: 41,
          }}
        >
          {children}
        </RadixDialog.Content>
      </RadixDialog.Portal>
    </RadixDialog.Root>
  );
}

export function DialogTitle({ children }: { children: React.ReactNode }) {
  return (
    <RadixDialog.Title
      style={{
        fontSize: 18,
        fontWeight: 700,
        margin: 0,
        padding: '18px 20px',
      }}
    >
      {children}
    </RadixDialog.Title>
  );
}

export function DialogContent({ children }: { children: React.ReactNode }) {
  return <div style={{ padding: '0 20px 20px' }}>{children}</div>;
}

export function DialogActions({ children }: { children: React.ReactNode }) {
  return (
    <div
      style={sxToStyle({
        borderTop: 1,
        display: 'flex',
        gap: 1,
        justifyContent: 'flex-end',
        p: 2.5,
      })}
    >
      {children}
    </div>
  );
}
