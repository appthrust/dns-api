import {
  EmptyState,
  OverviewPage,
  Page,
  PlatformCloudflareIntegrationNewPage,
  PlatformIntegrationDeletePage,
  PlatformIntegrationDetailPage,
  PlatformIntegrationEditPage,
  PlatformIntegrationsPage,
  PlatformIntegrationTypeSelectPage,
  PlatformPage,
  PlatformRoute53IntegrationNewPage,
  PlatformZoneClassDeletePage,
  PlatformZoneClassDetailPage,
  PlatformZoneClassEditPage,
  PlatformZoneClassesPage,
  PlatformZoneClassIdentitySelectPage,
  PlatformZoneClassNewPage,
  RecordSetDeleteRoutePage,
  RecordSetDetailRoutePage,
  RecordSetEditPage,
  RecordSetNewPage,
  ToolbarButton,
  ZoneClassSelectPage,
  ZoneDeletePage,
  ZoneDetailPage,
  ZoneEditPage,
  ZoneNewPage,
  ZonesPage,
} from '@appthrust/dns-api-ui';
import { Activity, CommonComponents } from '@kinvolk/headlamp-plugin/lib';
import type {
  KubeObject,
  KubeObjectClass,
  KubeObjectInterface,
} from '@kinvolk/headlamp-plugin/lib/lib/k8s/KubeObject';
import React from 'react';
import { useParams } from 'react-router-dom';
import {
  CloudflareIdentityResource,
  RecordSetResource,
  Route53IdentityResource,
  ZoneClassResource,
  ZoneResource,
} from './platform';
import { sidebarEntries } from './sidebarEntries';

export const pluginName = 'headlamp-plugin-dns-api';
export { sidebarEntries };

type YamlEditorRouteParams = {
  cluster?: string;
  group?: string;
  version?: string;
  kind?: string;
  namespace?: string;
  name?: string;
};

type ApiErrorLike = {
  message?: string;
  status?: number;
};

type EditableResourceClass = KubeObjectClass & {
  useGet: (
    name: string,
    namespace?: string,
    opts?: { cluster?: string }
  ) => [KubeObject<KubeObjectInterface> | null, ApiErrorLike | null];
};

const editableResourceClasses = new Map<string, EditableResourceClass>([
  ['dns.appthrust.io/v1alpha1/Zone', ZoneResource as EditableResourceClass],
  ['dns.appthrust.io/v1alpha1/ZoneClass', ZoneClassResource as EditableResourceClass],
  ['dns.appthrust.io/v1alpha1/RecordSet', RecordSetResource as EditableResourceClass],
  [
    'route53.dns.appthrust.io/v1alpha1/Route53Identity',
    Route53IdentityResource as EditableResourceClass,
  ],
  [
    'cloudflare.dns.appthrust.io/v1alpha1/CloudflareIdentity',
    CloudflareIdentityResource as EditableResourceClass,
  ],
]);

function routeParam(value?: string): string {
  return value ? decodeURIComponent(value) : '';
}

function apiVersionOf(group: string, version: string): string {
  return group && group !== '-' ? `${group}/${version}` : version;
}

function updateErrorMessage(error: unknown): string {
  const status =
    error && typeof error === 'object' && 'status' in error
      ? (error as ApiErrorLike).status
      : undefined;
  const message =
    error && typeof error === 'object' && 'message' in error
      ? (error as ApiErrorLike).message
      : undefined;

  if (status === 408) {
    return 'Conflicts when trying to perform operation (code 408).';
  }
  if (typeof status === 'number') {
    return `Failed to perform operation: code ${status}`;
  }
  return message || 'Failed to perform operation.';
}

function YamlEditorActivityContent({
  activityId,
  resourceClass,
  name,
  namespace,
  cluster,
}: {
  activityId: string;
  resourceClass: EditableResourceClass;
  name: string;
  namespace?: string;
  cluster?: string;
}) {
  const [item, error] = resourceClass.useGet(name, namespace, { cluster });
  const [errorMessage, setErrorMessage] = React.useState('');

  function handleSave(items: KubeObjectInterface | KubeObjectInterface[]) {
    const newItem = Array.isArray(items) ? items[0] : items;
    setErrorMessage('');
    void item
      ?.update(newItem)
      .then(() => Activity.close(activityId))
      .catch(err => setErrorMessage(updateErrorMessage(err)));
  }

  if (error) {
    return React.createElement(EmptyState, {
      title: 'YAML editor failed',
      body: error.message || 'The resource could not be loaded.',
    });
  }

  return React.createElement(CommonComponents.EditorDialog, {
    noDialog: true,
    open: true,
    item: item?.getEditableObject() ?? null,
    onClose: () => Activity.close(activityId),
    onSave: handleSave,
    allowToHideManagedFields: true,
    errorMessage,
    onEditorChanged: () => setErrorMessage(''),
  });
}

function toolbarOpenEditor(launchEditor: () => void) {
  return React.createElement(ToolbarButton, {
    label: 'Open editor',
    icon: 'mdi:pencil',
    tone: 'secondary',
    onClick: launchEditor,
  });
}

function yamlEditorUnavailablePage() {
  return React.createElement(Page, {
    title: 'YAML editor not available',
    description: 'This resource type is not supported by the DNS plugin YAML editor route.',
    children: React.createElement(EmptyState, {
      title: 'YAML editor not available',
      body: 'Open this editor from a supported DNS resource.',
    }),
  });
}

function yamlEditorOpenPage(
  kind: string,
  namespace: string,
  name: string,
  launchEditor: () => void
) {
  return React.createElement(Page, {
    title: `Edit ${kind || 'resource'} YAML`,
    description: `${namespace || '-'} / ${name}`,
    actions: toolbarOpenEditor(launchEditor),
    children: React.createElement(EmptyState, {
      title: 'YAML editor is open',
      body: 'Use the full screen editor to review or update this resource YAML.',
      action: toolbarOpenEditor(launchEditor),
    }),
  });
}

function YamlEditorRoutePage() {
  const params = useParams<YamlEditorRouteParams>();
  const group = routeParam(params.group);
  const version = routeParam(params.version);
  const kind = routeParam(params.kind);
  const namespace = routeParam(params.namespace);
  const name = routeParam(params.name);
  const clusterPath = routeParam(params.cluster);
  const cluster = clusterPath ? clusterPath.split('+')[0] : undefined;
  const apiVersion = apiVersionOf(group, version);
  const resourceClass = editableResourceClasses.get(`${apiVersion}/${kind}`);
  const activityId = `dns-api-yaml-editor-${cluster || '-'}-${apiVersion}-${kind}-${
    namespace || '-'
  }-${name}`;

  const launchEditor = React.useCallback(() => {
    if (!resourceClass || !name) {
      return;
    }
    Activity.launch({
      id: activityId,
      title: `Edit: ${name}`,
      icon: React.createElement('span', null, 'YAML'),
      cluster,
      location: 'full',
      content: React.createElement(YamlEditorActivityContent, {
        activityId,
        resourceClass,
        name,
        namespace: namespace === '-' ? undefined : namespace,
        cluster,
      }),
    });
  }, [activityId, cluster, name, namespace, resourceClass]);

  React.useEffect(() => {
    launchEditor();
  }, [launchEditor]);

  if (!resourceClass || !name) {
    return yamlEditorUnavailablePage();
  }

  return yamlEditorOpenPage(kind, namespace, name, launchEditor);
}

export const routes: Array<{
  name: string;
  path: string;
  sidebar: string;
  component: React.ComponentType;
}> = [
  { name: 'dns-api-overview', path: '/dns', sidebar: 'dns-api', component: OverviewPage },
  {
    name: 'dns-api-yaml-editor',
    path: '/dns/yaml/:group/:version/:kind/:namespace/:name',
    sidebar: 'dns-api',
    component: YamlEditorRoutePage,
  },
  { name: 'dns-api-zones', path: '/dns/zones', sidebar: 'dns-api-zones', component: ZonesPage },
  {
    name: 'dns-api-zone-new',
    path: '/dns/zones/new',
    sidebar: 'dns-api-zones',
    component: ZoneClassSelectPage,
  },
  {
    name: 'dns-api-zone-new-with-zoneclass',
    path: '/dns/zones/new/:zoneClassNamespace/:zoneClassName',
    sidebar: 'dns-api-zones',
    component: ZoneNewPage,
  },
  {
    name: 'dns-api-recordset-new',
    path: '/dns/zones/:namespace/:name/recordsets/new',
    sidebar: 'dns-api-zones',
    component: RecordSetNewPage,
  },
  {
    name: 'dns-api-recordset-edit',
    path: '/dns/zones/:namespace/:name/recordsets/:recordNamespace/:recordName/edit',
    sidebar: 'dns-api-zones',
    component: RecordSetEditPage,
  },
  {
    name: 'dns-api-recordset-delete',
    path: '/dns/zones/:namespace/:name/recordsets/:recordNamespace/:recordName/delete',
    sidebar: 'dns-api-zones',
    component: RecordSetDeleteRoutePage,
  },
  {
    name: 'dns-api-recordset-detail',
    path: '/dns/zones/:namespace/:name/recordsets/:recordNamespace/:recordName',
    sidebar: 'dns-api-zones',
    component: RecordSetDetailRoutePage,
  },
  {
    name: 'dns-api-zone-edit',
    path: '/dns/zones/:namespace/:name/edit',
    sidebar: 'dns-api-zones',
    component: ZoneEditPage,
  },
  {
    name: 'dns-api-zone-delete',
    path: '/dns/zones/:namespace/:name/delete',
    sidebar: 'dns-api-zones',
    component: ZoneDeletePage,
  },
  {
    name: 'dns-api-zone-detail',
    path: '/dns/zones/:namespace/:name',
    sidebar: 'dns-api-zones',
    component: ZoneDetailPage,
  },
  {
    name: 'dns-api-platform-root',
    path: '/dns/platform',
    sidebar: 'dns-api-platform-integrations',
    component: PlatformPage,
  },
  {
    name: 'dns-api-platform-integrations',
    path: '/dns/platform/integrations',
    sidebar: 'dns-api-platform-integrations',
    component: PlatformIntegrationsPage,
  },
  {
    name: 'dns-api-platform-integration-new',
    path: '/dns/platform/integrations/new',
    sidebar: 'dns-api-platform-integrations',
    component: PlatformIntegrationTypeSelectPage,
  },
  {
    name: 'dns-api-platform-integration-new-route53',
    path: '/dns/platform/integrations/new/route53',
    sidebar: 'dns-api-platform-integrations',
    component: PlatformRoute53IntegrationNewPage,
  },
  {
    name: 'dns-api-platform-integration-new-cloudflare',
    path: '/dns/platform/integrations/new/cloudflare',
    sidebar: 'dns-api-platform-integrations',
    component: PlatformCloudflareIntegrationNewPage,
  },
  {
    name: 'dns-api-platform-integration-edit',
    path: '/dns/platform/integrations/:namespace/:name/edit',
    sidebar: 'dns-api-platform-integrations',
    component: PlatformIntegrationEditPage,
  },
  {
    name: 'dns-api-platform-integration-delete',
    path: '/dns/platform/integrations/:namespace/:name/delete',
    sidebar: 'dns-api-platform-integrations',
    component: PlatformIntegrationDeletePage,
  },
  {
    name: 'dns-api-platform-integration-detail',
    path: '/dns/platform/integrations/:namespace/:name',
    sidebar: 'dns-api-platform-integrations',
    component: PlatformIntegrationDetailPage,
  },
  {
    name: 'dns-api-platform-zoneclasses',
    path: '/dns/platform/zoneclasses',
    sidebar: 'dns-api-platform-zoneclasses',
    component: PlatformZoneClassesPage,
  },
  {
    name: 'dns-api-platform-zoneclass-new',
    path: '/dns/platform/zoneclasses/new',
    sidebar: 'dns-api-platform-zoneclasses',
    component: PlatformZoneClassIdentitySelectPage,
  },
  {
    name: 'dns-api-platform-zoneclass-new-route53',
    path: '/dns/platform/zoneclasses/new/:identityNamespace/:identityName',
    sidebar: 'dns-api-platform-zoneclasses',
    component: PlatformZoneClassNewPage,
  },
  {
    name: 'dns-api-platform-zoneclass-edit',
    path: '/dns/platform/zoneclasses/:namespace/:name/edit',
    sidebar: 'dns-api-platform-zoneclasses',
    component: PlatformZoneClassEditPage,
  },
  {
    name: 'dns-api-platform-zoneclass-delete',
    path: '/dns/platform/zoneclasses/:namespace/:name/delete',
    sidebar: 'dns-api-platform-zoneclasses',
    component: PlatformZoneClassDeletePage,
  },
  {
    name: 'dns-api-platform-zoneclass-detail',
    path: '/dns/platform/zoneclasses/:namespace/:name',
    sidebar: 'dns-api-platform-zoneclasses',
    component: PlatformZoneClassDetailPage,
  },
];
