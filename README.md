# dns-api

dns-api lets Kubernetes users manage DNS zones and DNS records with Kubernetes resources.

It is useful when platform teams want to give application teams a safe DNS workflow without giving them direct access to AWS Route 53 or Cloudflare. Platform teams define which DNS providers, hosted zones, and record names are allowed. Application teams create `Zone` and `RecordSet` resources in their own namespaces.

dns-api is currently focused on:

- Amazon Route 53 public hosted zones, and adoption of existing private hosted zones
- Cloudflare full zones
- `A`, `AAAA`, `TXT`, `CNAME`, `MX`, `CAA`, and delegated `NS` records
- Route 53 `ALIAS` records
- A Headlamp Desktop plugin for browsing and editing dns-api resources

## How It Works

dns-api adds custom resources to your cluster:

- `Provider` describes a DNS provider and its supported features.
- `Route53Identity` and `CloudflareIdentity` tell the controller how to use provider credentials.
- `ZoneClass` is a platform policy. It decides which namespaces may create zones and which provider settings they use.
- `Zone` represents one DNS zone, such as `apps.example.com`.
- `RecordSet` represents one DNS record set inside a `Zone`.

The controller watches those resources and reconciles the real DNS provider state.

## Install

The controller is distributed as a container image and a Helm chart on GitHub Container Registry. Choose the released version you want to run:

```sh
VERSION=0.2.1

helm install dns-api oci://ghcr.io/appthrust/charts/dns-api \
  --namespace dns-api-system \
  --create-namespace \
  --version "$VERSION"
```

By default, the chart installs CRDs, the controller, RBAC, webhook resources, cert-manager integration, and the built-in Route 53 and Cloudflare provider definitions.

The chart uses the matching controller image tag by default:

```text
ghcr.io/appthrust/dns-api:<version>
```

For local development or source installs, use the chart in [deploy/charts/dns-api](./deploy/charts/dns-api).

### Customer-managed DNS (external provider)

The external provider is opt-in. It verifies customer-managed `A`, `AAAA`, and
`CNAME` records without creating, updating, or deleting DNS records and needs no
identity or provider credentials. Install cert-manager first, then save these
values as `external-values.yaml` and install from this checkout:

```yaml
providers:
  route53:
    enabled: false
  cloudflare:
    enabled: false
  external:
    enabled: true
    controllerName: external.dns.appthrust.io/controller
    providerName: external.dns.appthrust.io
    providerVersion: v1alpha1
    dnsQPS: 40
    dnsBurst: 80
webhook:
  enabled: true
certManager:
  enabled: true
```

```sh
helm install dns-api ./deploy/charts/dns-api \
  --namespace dns-api-system \
  --create-namespace \
  --values external-values.yaml
```

Use a controller image built from the same revision (or a release containing the
external provider). The chart rejects external enablement without both webhook
and cert-manager support: endpoint conversion uses the webhook Service with
cert-manager CA injection, never a TLS-verification bypass. External-only installs
grant no cloud identity or Secret API permissions; serving TLS certificates are
mounted by Kubernetes. External ZoneClasses must omit `identityRef`.

The chart values map to `--external-controller-enabled`,
`--external-controller-name`, `--external-provider-name`,
`--external-provider-version`, `--external-dns-qps`, and `--external-dns-burst`.
When running the manager directly, the first four accept environment defaults
`EXTERNAL_CONTROLLER_ENABLED`, `EXTERNAL_CONTROLLER_NAME`,
`EXTERNAL_PROVIDER_NAME`, and `EXTERNAL_PROVIDER_VERSION`; explicit flags win.
The defaults are the values above except enablement, which defaults to `false`.
DNS query limits apply per controller process.

For Kustomize, `app/operator/config/default` remains external-disabled.
`app/operator/config/external` is the opt-in overlay that adds external controllers,
Provider, capability, TLS-backed APIService, and conversion-create permission to
the default Route 53/Cloudflare installation. Disabling cloud providers in a
custom overlay must also remove their identity admission entries and RBAC (and
Cloudflare's Secret permission).

For a dedicated-member platform with base domain `B` and **M Projects**, customers
maintain **M+2 DNS record names**: `B` and `*.B` point at the PMC public edge, and
each `*.<Project>.B` points at that Project's actual serving Gateway addresses.
Publish the required A/AAAA sets (or a supported CNAME) for each name; do not point
a shared wildcard at the union of reserved member VIPs. Verification requires
exact target sets, including the absence of unexpected address-family answers.
Wildcard sampling does not replace probing each exact application hostname.
Cross-namespace wildcard claims also require an explicit `allowedRecordSets.records`
grant. See the [external provider design](./docs/design/external-provider.md) for
resolver policy, readiness, and wildcard limits.

## First Resources

A typical setup has two sides.

Platform team:

```yaml
apiVersion: route53.dns.appthrust.io/v1alpha1
kind: Route53Identity
metadata:
  namespace: tenant-a-platform
  name: production
spec:
  accountID: "123456789012"
  region: ap-northeast-1
  credentials:
    runtime: {}
---
apiVersion: dns.appthrust.io/v1alpha1
kind: ZoneClass
metadata:
  namespace: tenant-a-platform
  name: route53-public
spec:
  provider:
    name: route53.dns.appthrust.io
    version: v1alpha1
  controllerName: route53.dns.appthrust.io/controller
  identityRef:
    name: production
  allowedZones:
    namespaces:
      from: Selector
      selector:
        matchLabels:
          appthrust.io/tenant: tenant-a
  parameters:
    zoneCreationPolicy: Create
    zoneDeletionPolicy: Retain
```

Application team:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: Zone
metadata:
  namespace: tenant-a-app
  name: apps-example-com
spec:
  domainName: apps.example.com
  provider:
    name: route53.dns.appthrust.io
    version: v1alpha1
  zoneClassRef:
    namespace: tenant-a-platform
    name: route53-public
---
apiVersion: dns.appthrust.io/v1alpha1
kind: RecordSet
metadata:
  namespace: tenant-a-app
  name: www
spec:
  zoneRef:
    name: apps-example-com
  provider:
    name: route53.dns.appthrust.io
    version: v1alpha1
  type: A
  name: www
  ttl: 300
  a:
    addresses:
      - 192.0.2.10
```

More examples are in [app/operator/config/samples](./app/operator/config/samples).

## Headlamp Plugin

`headlamp-plugin-dns-api` adds dns-api screens to Headlamp Desktop.

The plugin uses the Kubernetes cluster selected in Headlamp. It does not call AWS, Route 53, Cloudflare, or other DNS provider APIs directly. It also does not store provider credentials in Headlamp Desktop.

Install the dns-api controller and CRDs in the target cluster before using the plugin. Installing the Headlamp plugin does not install the controller.

Plugin source lives in [app/headlamp-plugin](./app/headlamp-plugin). Shared UI code lives in [pkg/ts/ui](./pkg/ts/ui).

## Repository Layout

```text
app/operator          Kubernetes controller, manifests, and e2e tests
app/headlamp-plugin   Headlamp Desktop plugin
app/ui-mockup         UI mockup app
internal/go           Go code used only inside this repository
pkg/go/api            Go API packages for dns-api custom resources
pkg/ts/ui             TypeScript UI package shared by the plugin and mockup
deploy/charts         Helm chart
deploy/artifacthub    Artifact Hub metadata
docs/design           Design documents
```

## Development

Development tools are managed by devbox. The main commands are in [Taskfile.yml](./Taskfile.yml).

```sh
task up
task test
task ci
```

`task up` creates or updates the local kind cluster and runs Tilt. The controller and webhook run inside the cluster.

## Design Documents

Current design notes are in [docs/design](./docs/design). Start with:

- [Overview](./docs/design/overview.md)
- [Core API](./docs/design/core-api.md)
- [Route 53 provider](./docs/design/route53-provider.md)
- [Cloudflare provider](./docs/design/cloudflare-provider.md)
- [Headlamp plugin](./docs/design/headlamp-plugin.md)

## License

dns-api is licensed under Apache-2.0. See [LICENSE](./LICENSE).
