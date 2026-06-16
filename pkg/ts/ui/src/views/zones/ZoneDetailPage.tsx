/** @jsxRuntime classic */
import React from 'react';
import { requestZoneReconcile, useAccess, useDnsData } from '../../api/dns';
import { Box, IconButton } from '../../components/primitives';
import { Icon } from '../../components/Icon';
import { nameOf, namespaceOf, RecordSetResource, ZoneResource } from '../../resources';
import {
  descriptionOf,
  EmptyState,
  Page,
  ToolbarButton,
  ui,
  useNotice,
} from '../common/ui';
import {
  findZone,
  recordSetNewPath,
  recordSetPath,
  useZoneRouteParams,
  useZonesNavigation,
  zoneDeletePath,
  zoneEditPath,
  ZONES_BREADCRUMB,
} from './routes';
import { ZoneDetails } from './ZoneDetails';

export function ZoneDetailPage() {
  const navigation = useZonesNavigation();
  const data = useDnsData();
  const params = useZoneRouteParams();
  const { showSuccess, showError, snackbar } = useNotice();
  const [reconcilePending, setReconcilePending] = React.useState(false);
  const canCreateRecordSet = useAccess(RecordSetResource, 'create', {
    group: 'dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'recordsets',
  });
  const canRequestReconcile = useAccess(ZoneResource, 'patch', {
    group: 'dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'zones',
    namespace: params.namespace,
    name: params.name,
  });
  const canDelete = useAccess(ZoneResource, 'delete', {
    group: 'dns.appthrust.io',
    version: 'v1alpha1',
    resource: 'zones',
  });
  const zone = findZone(data.zones.items, params.namespace, params.name);

  if (!zone) {
    return (
      <Page
        breadcrumb={[ZONES_BREADCRUMB]}
        title="Zone not found"
        description="The requested Zone is not visible in this cluster."
      >
        <EmptyState
          title="Zone not found"
          body="The requested Zone is not visible in this cluster."
        />
      </Page>
    );
  }
  const selectedZone = zone;

  async function handleReconcileNow() {
    setReconcilePending(true);
    try {
      await requestZoneReconcile(selectedZone, new Date().toISOString());
      showSuccess('Reconcile requested');
    } catch (error) {
      showError(error);
    } finally {
      setReconcilePending(false);
    }
  }

  return (
    <Page
      breadcrumb={[ZONES_BREADCRUMB]}
      title={selectedZone.spec.domainName}
      description={
        descriptionOf(selectedZone) || `${namespaceOf(selectedZone)}/${nameOf(selectedZone)}`
      }
      actions={
        <>
          <ToolbarButton
            label="Add RecordSet"
            icon="mdi:plus"
            disabled={!canCreateRecordSet}
            onClick={() => navigation.push(recordSetNewPath(selectedZone))}
          />
          <ToolbarButton
            label="Edit"
            icon="mdi:pencil"
            tone="secondary"
            onClick={() => navigation.push(zoneEditPath(selectedZone))}
          />
          <ZoneActionMenu
            canReconcile={canRequestReconcile}
            canDelete={canDelete}
            reconcilePending={reconcilePending}
            onReconcile={handleReconcileNow}
            onDelete={() => navigation.push(zoneDeletePath(selectedZone))}
          />
        </>
      }
    >
      <ZoneDetails
        zone={selectedZone}
        data={data}
        onCreateRecordSet={() => navigation.push(recordSetNewPath(selectedZone))}
        onOpenRecordSet={recordSet => navigation.push(recordSetPath(selectedZone, recordSet))}
      />
      {snackbar}
    </Page>
  );
}

function ZoneActionMenu({
  canReconcile,
  canDelete,
  reconcilePending,
  onReconcile,
  onDelete,
}: {
  canReconcile: boolean;
  canDelete: boolean;
  reconcilePending: boolean;
  onReconcile: () => Promise<void>;
  onDelete: () => void;
}) {
  const [open, setOpen] = React.useState(false);
  const menuRef = React.useRef<HTMLDivElement | null>(null);

  React.useEffect(() => {
    if (!open) {
      return undefined;
    }

    function closeOnOutside(event: PointerEvent) {
      if (menuRef.current?.contains(event.target as Node)) {
        return;
      }
      setOpen(false);
    }

    function closeOnEscape(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setOpen(false);
      }
    }

    document.addEventListener('pointerdown', closeOnOutside);
    document.addEventListener('keydown', closeOnEscape);
    return () => {
      document.removeEventListener('pointerdown', closeOnOutside);
      document.removeEventListener('keydown', closeOnEscape);
    };
  }, [open]);

  return (
    <div ref={menuRef} style={{ position: 'relative' }}>
      <IconButton
        aria-label="Resource actions"
        onClick={() => setOpen(current => !current)}
        sx={{ borderColor: ui.border, color: ui.text }}
      >
        <Icon icon="mdi:dots-horizontal" />
      </IconButton>
      {open ? (
        <Box
          sx={{
            bgcolor: ui.panelBg,
            border: 1,
            borderColor: ui.border,
            borderRadius: 1,
            boxShadow: '0 12px 32px rgba(15, 23, 42, 0.16)',
            minWidth: 180,
            p: 0.75,
            position: 'absolute',
            right: 0,
            top: 38,
            zIndex: 20,
          }}
        >
          <MenuButton
            disabled={!canReconcile || reconcilePending}
            onClick={() => {
              setOpen(false);
              void onReconcile();
            }}
          >
            Reconcile now
          </MenuButton>
          <Box sx={{ borderTop: 1, borderColor: ui.borderSoft, my: 0.5 }} />
          <MenuButton
            danger
            disabled={!canDelete}
            onClick={() => {
              setOpen(false);
              onDelete();
            }}
          >
            Delete Zone
          </MenuButton>
        </Box>
      ) : null}
    </div>
  );
}

function MenuButton({
  children,
  danger,
  disabled,
  onClick,
}: {
  children: React.ReactNode;
  danger?: boolean;
  disabled?: boolean;
  onClick: () => void;
}) {
  return (
    <button
      disabled={disabled}
      onClick={onClick}
      style={{
        background: 'transparent',
        border: 0,
        borderRadius: 'var(--dns-ui-radius-sm, 4px)',
        color: danger ? 'var(--dns-ui-danger, #b42318)' : 'var(--dns-ui-text, #111827)',
        cursor: disabled ? 'not-allowed' : 'pointer',
        display: 'block',
        font: 'inherit',
        opacity: disabled ? 0.5 : 1,
        padding: '8px 10px',
        textAlign: 'left',
        width: '100%',
      }}
    >
      {children}
    </button>
  );
}
