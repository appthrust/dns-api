# Overview

dns-api is an API for declaring DNS zones and DNS record sets as Kubernetes resources and reconciling them to DNS providers with a consistent user experience. AppThrust DNS Operator is one controller implementation of this API.

The API follows the same broad model as Gateway API: provider differences are expressed as Kubernetes resources and conditions. Gateway API `GatewayClass` corresponds to `ZoneClass`, `Gateway` corresponds to `Zone`, and route-like resources correspond to `RecordSet`.

ExternalDNS is a controller that creates DNS records from existing Kubernetes resources. dns-api manages DNS zones and DNS record sets themselves as Kubernetes resources.

## Initial Scope

The initial implementation targets Route 53 public hosted zones. dns-api handles Public DNS only. Private DNS, VPC association, split-horizon DNS, and DNS propagation checks are out of scope initially. The API is not Route 53-specific and must allow future providers such as Google Cloud DNS, Cloudflare, and Azure DNS.

The initial milestone is the ability to use a development AWS profile to create public hosted zones, standard A records, standard AAAA records, standard TXT records, standard CNAME records, standard MX records, standard CAA records, delegated NS records, and Route 53 ALIAS records from `Provider`, `ZoneClass`, `Route53Identity`, `Zone`, and `RecordSet`. DNS propagation checks are not part of this milestone.

Provider-specific data whose lifecycle matches a parent resource is modeled as an inline object on that parent resource. Only data with a different lifecycle, permission boundary, reuse unit, or atomic acquisition requirement becomes a separate CRD. For Route 53, ZoneClass settings, Zone provider status, RecordSet provider options, and RecordSet provider status are inline objects. AWS identity is a separate `Route53Identity` CRD.

Core API resources:

- `ZoneClass`: a DNS zone creation menu.
- `Provider`: provider capability, schema, validation, and display metadata.
- `Zone`: a management unit corresponding to a hosted zone / zone on the DNS provider.
- `RecordSet`: a DNS record set.
- `ZoneUnit`: the zone-scoped desired-state and ownership ledger built from accepted `Zone` and `RecordSet` claims.
- Provider-specific identity CRDs: which provider account, IAM role, or service account is used for operations.

Provider identity details live in provider-specific identity CRDs. `ZoneClass.spec.identityRef` selects a provider identity by name, and `ZoneClass.spec.parameters` holds provider-specific policy as an inline object. The core API has no provider-specific account or credential schema. The provider version declares the identity resource kind so UI, documentation, and generic reference display can discover which identity kind a `ZoneClass` uses.

Standard condition types are `Accepted` and `Programmed`. `Accepted` is the result of composition acceptance and provider acceptance. `Programmed` describes whether the external provider's observed state matches desired state. Conditions such as `Propagated` or `Resolvable` that require DNS-query observation are not part of the initial core API.
