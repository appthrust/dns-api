import * as RadixSelect from '@radix-ui/react-select';
/** @jsxRuntime classic */
import React from 'react';
import { ChevronDown, Check } from 'lucide-react';
import { sxToStyle, token, type Sx } from './style';

type SelectChangeEvent = {
  target: {
    value: string;
  };
};

const emptyValue = '__dns-ui-empty-select-value__';

function toRadixValue(value: string | undefined) {
  return value === '' ? emptyValue : value;
}

function fromRadixValue(value: string) {
  return value === emptyValue ? '' : value;
}

export function Select({
  label,
  value,
  onChange,
  disabled,
  error,
  children,
  sx,
  style,
  inputProps,
  renderValue,
}: {
  label?: string;
  value?: string;
  onChange?: (event: SelectChangeEvent) => void;
  disabled?: boolean;
  error?: boolean;
  children: React.ReactNode;
  sx?: Sx;
  style?: React.CSSProperties;
  displayEmpty?: boolean;
  inputProps?: { 'aria-label'?: string };
  renderValue?: (value: string | undefined) => React.ReactNode;
}) {
  const [open, setOpen] = React.useState(false);

  React.useEffect(() => {
    return () => {
      if (open && document.body.style.pointerEvents === 'none') {
        document.body.style.pointerEvents = '';
      }
    };
  }, [open]);

  return (
    <RadixSelect.Root
      open={open}
      value={toRadixValue(value)}
      disabled={disabled}
      onOpenChange={setOpen}
      onValueChange={nextValue => onChange?.({ target: { value: fromRadixValue(nextValue) } })}
    >
      <RadixSelect.Trigger
        aria-label={inputProps?.['aria-label'] ?? label}
        style={{
          alignItems: 'center',
          background: token('fieldBg'),
          border: `1px solid ${error ? token('danger') : token('border')}`,
          borderRadius: 'var(--dns-ui-radius-sm, 4px)',
          color: token('text'),
          cursor: disabled ? 'not-allowed' : 'pointer',
          display: 'inline-flex',
          font: 'inherit',
          justifyContent: 'space-between',
          minHeight: 'var(--dns-ui-control-height, 38px)',
          opacity: disabled ? 0.55 : 1,
          padding: '8px 10px',
          width: '100%',
          ...sxToStyle(sx),
          ...style,
        }}
      >
        {renderValue ? renderValue(value) : <RadixSelect.Value />}
        <RadixSelect.Icon>
          <ChevronDown size={16} />
        </RadixSelect.Icon>
      </RadixSelect.Trigger>
      <RadixSelect.Portal>
        <RadixSelect.Content
          position="popper"
          sideOffset={4}
          style={{
            background: token('surface'),
            border: `1px solid ${token('border')}`,
            borderRadius: 'var(--dns-ui-radius-sm, 4px)',
            boxShadow: '0 16px 44px rgba(15, 23, 42, 0.16)',
            color: token('text'),
            minWidth: 'var(--radix-select-trigger-width)',
            overflow: 'hidden',
            zIndex: 70,
          }}
        >
          <RadixSelect.Viewport>{children}</RadixSelect.Viewport>
        </RadixSelect.Content>
      </RadixSelect.Portal>
    </RadixSelect.Root>
  );
}

export function MenuItem({
  value,
  children,
  disabled,
}: {
  value: string;
  children: React.ReactNode;
  disabled?: boolean;
}) {
  return (
    <RadixSelect.Item
      disabled={disabled}
      value={toRadixValue(value) ?? emptyValue}
      style={{
        alignItems: 'center',
        cursor: 'pointer',
        display: 'flex',
        fontSize: 14,
        gap: 8,
        minHeight: 34,
        outline: 'none',
        padding: '7px 10px 7px 32px',
        position: 'relative',
      }}
    >
      <RadixSelect.ItemIndicator style={{ left: 10, position: 'absolute' }}>
        <Check size={14} />
      </RadixSelect.ItemIndicator>
      <RadixSelect.ItemText>{children}</RadixSelect.ItemText>
    </RadixSelect.Item>
  );
}
