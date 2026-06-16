import * as RadixAccordion from '@radix-ui/react-accordion';
/** @jsxRuntime classic */
import React from 'react';
import { sxToStyle, token, type Sx } from './style';

export function Accordion({
  defaultExpanded,
  children,
  sx,
  style,
}: {
  defaultExpanded?: boolean;
  children: React.ReactNode;
  sx?: Sx;
  style?: React.CSSProperties;
  variant?: string;
  disableGutters?: boolean;
}) {
  return (
    <RadixAccordion.Root
      type="single"
      collapsible
      defaultValue={defaultExpanded ? 'content' : undefined}
      style={{
        border: `1px solid ${token('border')}`,
        borderRadius: 'var(--dns-ui-radius-md, 8px)',
        overflow: 'hidden',
        ...sxToStyle(sx),
        ...style,
      }}
    >
      <RadixAccordion.Item value="content">{children}</RadixAccordion.Item>
    </RadixAccordion.Root>
  );
}

export function AccordionSummary({
  children,
  expandIcon,
}: {
  children: React.ReactNode;
  expandIcon?: React.ReactNode;
}) {
  return (
    <RadixAccordion.Header style={{ margin: 0 }}>
      <RadixAccordion.Trigger
        style={{
          alignItems: 'center',
          background: 'transparent',
          border: 0,
          color: token('text'),
          cursor: 'pointer',
          display: 'flex',
          font: 'inherit',
          justifyContent: 'space-between',
          padding: 12,
          textAlign: 'left',
          width: '100%',
        }}
      >
        {children}
        {expandIcon ? <span aria-hidden="true">{expandIcon}</span> : null}
      </RadixAccordion.Trigger>
    </RadixAccordion.Header>
  );
}

export function AccordionDetails({
  sx,
  style,
  children,
}: {
  sx?: Sx;
  style?: React.CSSProperties;
  children: React.ReactNode;
}) {
  return (
    <RadixAccordion.Content style={{ ...sxToStyle(sx), ...style }}>
      {children}
    </RadixAccordion.Content>
  );
}
