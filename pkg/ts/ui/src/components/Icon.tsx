/** @jsxRuntime classic */
import {
  Activity,
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  Check,
  CheckCircle2,
  ChevronDown,
  ChevronRight,
  Cloud,
  Copy,
  X,
  ExternalLink,
  MoreHorizontal,
  FileCode2,
  FileText,
  Globe2,
  KeyRound,
  Layers3,
  Network,
  Pencil,
  Plus,
  Circle,
  CircleHelp,
  ServerCog,
  Trash2,
  type LucideIcon,
} from 'lucide-react';
import React from 'react';

const iconComponents: Record<string, LucideIcon> = {
  'mdi:add': Plus,
  'mdi:alert-circle-outline': AlertTriangle,
  'mdi:alert-outline': AlertTriangle,
  'mdi:arrow-left': ArrowLeft,
  'mdi:arrow-right': ArrowRight,
  'mdi:check': Check,
  'mdi:check-circle-outline': CheckCircle2,
  'mdi:chevron-down': ChevronDown,
  'mdi:chevron-right': ChevronRight,
  'mdi:circle-outline': Circle,
  'mdi:close': X,
  'mdi:content-copy': Copy,
  'mdi:delete': Trash2,
  'mdi:dns': ServerCog,
  'mdi:dots-horizontal': MoreHorizontal,
  'mdi:earth': Globe2,
  'mdi:file-code-outline': FileCode2,
  'mdi:file-document-outline': FileText,
  'mdi:help-circle-outline': CircleHelp,
  'mdi:key-chain': KeyRound,
  'mdi:lan': Network,
  'mdi:layers': Layers3,
  'mdi:open-in-new': ExternalLink,
  'mdi:pencil': Pencil,
  'mdi:plus': Plus,
  'mdi:progress-clock': Activity,
  'simple-icons:amazonaws': Cloud,
  'simple-icons:cloudflare': Cloud,
  'simple-icons:googlecloud': Cloud,
};

export function Icon({
  icon,
  color,
  width,
}: {
  icon: string;
  color?: string;
  width?: number | string;
}) {
  const Component = iconComponents[icon] ?? ServerCog;
  const size = typeof width === 'number' ? width : width ? Number.parseInt(String(width), 10) : 18;
  return (
    <Component
      aria-hidden="true"
      data-icon={icon}
      color={color}
      focusable="false"
      size={Number.isFinite(size) ? size : 18}
      strokeWidth={2}
    />
  );
}
