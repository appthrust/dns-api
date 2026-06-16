# dns-api

dns-api lets Kubernetes users manage DNS zones and DNS records with Kubernetes resources.

It is useful when platform teams want to give application teams a safe DNS workflow without giving them direct access to AWS Route 53 or Cloudflare. Platform teams define which DNS providers, hosted zones, and record names are allowed. Application teams create `Zone` and `RecordSet` resources in their own namespaces.

dns-api is currently focused on:

- Amazon Route 53 public hosted zones
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
VERSION=0.1.0

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
