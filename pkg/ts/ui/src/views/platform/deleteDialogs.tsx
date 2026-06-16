/** @jsxRuntime classic */
import { Icon } from '../../components/Icon';
import { Alert } from '../../components/primitives';
import { Button } from '../../components/primitives';
import { Dialog } from '../../components/primitives';
import { DialogActions } from '../../components/primitives';
import { DialogContent } from '../../components/primitives';
import { DialogTitle } from '../../components/primitives';
import { Paper } from '../../components/primitives';
import { Stack } from '../../components/primitives';
import { Table } from '../../components/primitives';
import { TableBody } from '../../components/primitives';
import { TableCell } from '../../components/primitives';
import { TableContainer } from '../../components/primitives';
import { TableHead } from '../../components/primitives';
import { TableRow } from '../../components/primitives';
import React from 'react';
import { deleteByKey } from '../../api/dns';
import { nameOf, namespaceOf, resourceKey } from '../../resources';
import type { Route53Identity, Zone, ZoneClass } from '../../types/resources';
import { Conditions, DnsData, useNotice } from '../common/ui';
import { zoneClassesReferencingIdentity, zonesReferencingZoneClass } from './resources';

function ReferenceZonesTable({ zones }: { zones: Zone[] }) {
  if (!zones.length) {
    return <Alert severity="info">No referencing Zones found.</Alert>;
  }
  return (
    <TableContainer component={Paper} variant="outlined">
      <Table size="small">
        <TableHead>
          <TableRow>
            <TableCell>Namespace</TableCell>
            <TableCell>Name</TableCell>
            <TableCell>Domain</TableCell>
            <TableCell>Conditions</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {zones.map(zone => (
            <TableRow key={resourceKey(zone)}>
              <TableCell>{namespaceOf(zone)}</TableCell>
              <TableCell>{nameOf(zone)}</TableCell>
              <TableCell>{zone.spec.domainName}</TableCell>
              <TableCell>
                <Conditions conditions={zone.status?.conditions} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

function ReferenceZoneClassesTable({
  zoneClasses,
  zones,
}: {
  zoneClasses: ZoneClass[];
  zones: Zone[];
}) {
  if (!zoneClasses.length) {
    return <Alert severity="info">No referencing ZoneClasses found.</Alert>;
  }
  return (
    <TableContainer component={Paper} variant="outlined">
      <Table size="small">
        <TableHead>
          <TableRow>
            <TableCell>Namespace</TableCell>
            <TableCell>Name</TableCell>
            <TableCell>Zones</TableCell>
            <TableCell>Conditions</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {zoneClasses.map(zoneClass => {
            const referencingZones = zonesReferencingZoneClass(zones, zoneClass);
            return (
              <TableRow key={resourceKey(zoneClass)}>
                <TableCell>{namespaceOf(zoneClass)}</TableCell>
                <TableCell>{nameOf(zoneClass)}</TableCell>
                <TableCell>{referencingZones.length}</TableCell>
                <TableCell>
                  <Conditions conditions={zoneClass.status?.conditions} />
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
    </TableContainer>
  );
}

export function ZoneClassDeleteDialog({
  zoneClass,
  open,
  onClose,
  data,
}: {
  zoneClass: ZoneClass | null;
  open: boolean;
  onClose: () => void;
  data: DnsData;
}) {
  const { showSuccess, showError, snackbar } = useNotice();
  if (!zoneClass) {
    return null;
  }
  const selectedZoneClass = zoneClass;
  const referencingZones = zonesReferencingZoneClass(data.zones.items, selectedZoneClass);

  async function submit() {
    try {
      await deleteByKey(
        data.zoneClasses.objects,
        namespaceOf(selectedZoneClass),
        nameOf(selectedZoneClass)
      );
      showSuccess('ZoneClass deletion requested');
      onClose();
    } catch (error) {
      showError(error);
    }
  }

  return (
    <>
      <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
        <DialogTitle>Delete ZoneClass</DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ pt: 1 }}>
            <Alert severity="info">
              Deleting this ZoneClass does not directly delete provider hosted zones or record sets,
              but Zones that reference it can no longer reconcile normally.
            </Alert>
            {referencingZones.length ? (
              <Alert severity="warning">
                This ZoneClass is still referenced. Delete or move the Zones before deleting it.
              </Alert>
            ) : null}
            <ReferenceZonesTable zones={referencingZones} />
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            startIcon={<Icon icon="mdi:delete" />}
            color="error"
            variant="contained"
            disabled={referencingZones.length > 0}
            onClick={submit}
          >
            Delete
          </Button>
        </DialogActions>
      </Dialog>
      {snackbar}
    </>
  );
}

export function IdentityDeleteDialog({
  identity,
  open,
  onClose,
  data,
}: {
  identity: Route53Identity | null;
  open: boolean;
  onClose: () => void;
  data: DnsData;
}) {
  const { showSuccess, showError, snackbar } = useNotice();
  if (!identity) {
    return null;
  }
  const selectedIdentity = identity;
  const referencingZoneClasses = zoneClassesReferencingIdentity(
    data.zoneClasses.items,
    selectedIdentity
  );

  async function submit() {
    try {
      await deleteByKey(
        data.identities.objects,
        namespaceOf(selectedIdentity),
        nameOf(selectedIdentity)
      );
      showSuccess('Route53Identity deletion requested');
      onClose();
    } catch (error) {
      showError(error);
    }
  }

  return (
    <>
      <Dialog open={open} onClose={onClose} maxWidth="md" fullWidth>
        <DialogTitle>Delete Route53Identity</DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ pt: 1 }}>
            <Alert severity="info">
              Deleting this identity resource does not delete AWS IAM roles or hosted zones, but
              ZoneClasses and Zones that reference it can no longer reconcile normally.
            </Alert>
            {referencingZoneClasses.length ? (
              <Alert severity="warning">
                This Route53Identity is still referenced. Remove those ZoneClass references before
                deleting it.
              </Alert>
            ) : null}
            <ReferenceZoneClassesTable
              zoneClasses={referencingZoneClasses}
              zones={data.zones.items}
            />
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            startIcon={<Icon icon="mdi:delete" />}
            color="error"
            variant="contained"
            disabled={referencingZoneClasses.length > 0}
            onClick={submit}
          >
            Delete
          </Button>
        </DialogActions>
      </Dialog>
      {snackbar}
    </>
  );
}
