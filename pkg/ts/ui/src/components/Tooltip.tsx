import * as RadixTooltip from '@radix-ui/react-tooltip';
/** @jsxRuntime classic */
import React from 'react';
import { sxToStyle, token } from './style';

export function Tooltip({
  title,
  children,
}: {
  title?: React.ReactNode;
  children: React.ReactNode;
}) {
  if (!title) {
    return <>{children}</>;
  }

  return (
    <RadixTooltip.Provider delayDuration={220}>
      <RadixTooltip.Root>
        <RadixTooltip.Trigger asChild>
          <span style={{ display: 'inline-flex' }}>{children}</span>
        </RadixTooltip.Trigger>
        <RadixTooltip.Portal>
          <RadixTooltip.Content
            sideOffset={6}
            style={sxToStyle({
              bgcolor: 'text',
              borderRadius: 1,
              color: 'surface',
              fontSize: 12,
              maxWidth: 260,
              px: 1,
              py: 0.75,
              zIndex: 60,
            })}
          >
            {title}
            <RadixTooltip.Arrow fill={token('text')} />
          </RadixTooltip.Content>
        </RadixTooltip.Portal>
      </RadixTooltip.Root>
    </RadixTooltip.Provider>
  );
}
