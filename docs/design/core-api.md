# Core API

## ZoneClass

`ZoneClass` is a namespace-scoped resource. It represents provider implementation, provider identity selection, provider-specific inline parameters, and the allowed `Zone` namespace range.

`ZoneClass.spec` fields:

- `provider.name`: `Provider.metadata.name`. Immutable. Because it references cluster-scoped `Provider`, it does not include namespace or kind.
- `provider.version`: `Provider.spec.versions[].name`. Immutable.
- `controllerName`: provider controller instance that reconciles `ZoneUnit` resources reachable through this class. Required and immutable.
- `identityRef`: provider identity reference. Required. `identityRef.name` references the provider identity resource declared by the selected Provider version in the same namespace as the `ZoneClass`.
- `parameters`: provider-specific inline settings. Validated by the referenced Provider version `zoneClass.schemas.parameters`.
- `allowedZones`: namespace conditions for `Zone` resources that may use this `ZoneClass`.

`ZoneClass.spec.controllerName` uses the Gateway API-style domain-prefixed path form. It must include both a DNS subdomain prefix and a path part after `/`. Valid examples are `route53.dns.appthrust.io/controller` and `cloudflare.dns.appthrust.io/prod-a`; `route53.dns.appthrust.io` is invalid.

`ZoneClass.spec.identityRef` contains only `name`. It does not contain namespace, group, or kind. Cross-namespace identity references are not part of the initial design; identity resources live with the `ZoneClass` in the same platform namespace. The referenced Provider version declares the identity `group` and `kind`, so repeating them in every `ZoneClass` would create an unnecessary mismatch surface.

`ZoneClass.status.conditions` contains `Accepted`. `Accepted=True` means `provider`, `identityRef`, `parameters`, and `allowedZones` are statically acceptable. `Accepted=False` reasons are `InvalidProvider`, `InvalidIdentityRef`, `InvalidParameters`, and `DeniedByPolicy`. `Accepted=Unknown` reasons are `IdentityNotResolved`, `ProviderStorageVersionNotResolved`, and `ReconcileError`.

Example:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: ZoneClass
metadata:
  namespace: tenant-a-platform
  name: route53-public
spec:
  allowedZones:
    namespaces:
      from: Selector
      selector:
        matchLabels:
          appthrust.io/tenant: tenant-a
  provider:
    name: route53.dns.appthrust.io
    version: v1alpha1
  controllerName: route53.dns.appthrust.io/controller
  identityRef:
    name: route53-dev
  parameters:
    zoneCreationPolicy: Create
    zoneDeletionPolicy: Retain
    sameNameZonePolicy: Deny
    tags:
      appthrust.io/project: dns-api
      appthrust.io/environment: development
```

### Controller Responsibility

`ZoneClass` is not reconciled by a core controller. The core validating webhook reads the referenced Provider version and validates `spec.identityRef`, `spec.parameters`, and `spec.allowedZones`.

Provider controllers reconcile only `ZoneClass` resources whose `spec.controllerName` matches their own controller name. If it does not match, they do not update status. They interpret `identityRef` according to the Provider version identity resource declaration, interpret provider-specific `parameters`, and set `ZoneClass.status.conditions[Accepted]` to `True` when the class is statically acceptable.

## Zone

`Zone` is a namespace-scoped resource. It is the management unit corresponding to a hosted zone / zone on the DNS provider, connecting Kubernetes ownership boundaries with the provider-side object.

`Zone.spec` fields:

- `domainName`: zone apex. Only canonical ASCII values are accepted. Immutable.
- `zoneClassRef`: selected `ZoneClass`. If namespace is omitted, it defaults to the `Zone` namespace. Immutable.
- `provider.name`: `Provider.metadata.name`. Required and immutable.
- `provider.version`: `Provider.spec.versions[].name`. Required and immutable.
- `adoption`: specified when adopting an existing external zone. Omitted for creation. Mutable, but provider controllers stop changes that point to a different external zone after management starts.
- `adoption`: provider-specific opaque object validated by the referenced Provider version `zone.schemas.adoption`.
- `allowedRecordSets`: conditions under which `RecordSet` resources from other namespaces may attach. If omitted, only same-namespace `RecordSet` resources are allowed. Each rule combines a namespace selector and record policy.

`Zone.spec.provider` duplicates `ZoneClass.spec.provider` so GitOps can apply `Zone` and `ZoneClass` in any order while still validating `Zone.spec.adoption` and Zone provider-specific validation rules. Admission validates the declared Provider version and provider-specific payloads, but it does not reject a `Zone` because the referenced `ZoneClass` is absent or because `Zone.spec.provider` differs from `ZoneClass.spec.provider`. The Core ZoneUnit Controller reports those reference and Provider consistency problems later as `Accepted=Unknown` or `Accepted=False`.

`managementPolicy`, `useExisting`, and `protected` are not part of the initial `Zone.spec`.

`Zone` deletion policy is provider-specific ZoneClass policy. For Route 53 it is `ZoneClass.spec.parameters.zoneDeletionPolicy`. The core `Zone.spec` has no deletion policy.

### Controller Responsibility

`Zone` is a claim reconciled by the Core ZoneUnit Controller. The core validating webhook validates `domainName`, `zoneClassRef`, `provider`, `adoption`, and the object-local shape of `allowedRecordSets`.

The Core ZoneUnit Controller resolves `ZoneClass` from `zoneClassRef`, checks `Zone.spec.provider` against `ZoneClass.spec.provider`, applies `ZoneClass.spec.allowedZones`, converts provider payload to Provider storage version, and writes the accepted desired state to `ZoneUnit.spec.zone`. Provider controllers do not reconcile `Zone` directly and do not write `Zone.status`; they read `ZoneUnit.spec` and write `ZoneUnit.status`. The Core ZoneUnit Controller projects `ZoneUnit.status.zone` back to `Zone.status`.

Example:

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
  allowedRecordSets:
    - namespaces:
        selector:
          matchLabels:
            appthrust.io/dns-access: apps-example-com
      records:
        - name:
            pattern: '^app-a$'
          types:
            - A
            - AAAA
            - CNAME
```

## allowedRecordSets

`allowedRecordSets` interpretation:

- Rules are evaluated as OR.
- Within one rule, `namespaces` and `records` are evaluated as AND.
- `namespaces.selector` is evaluated against labels on the namespace containing the `RecordSet`.
- Entries in `records` are evaluated as OR.
- `records[].name.pattern` is evaluated against the normalized `RecordSet.spec.name`.
- `records[].types` is an allow list for `RecordSet.spec.type`.
- Direct namespace name lists are not provided. Even a single namespace is selected through namespace labels.

Schema shape:

```yaml
allowedRecordSets:
  - namespaces:
      selector:
        matchLabels:
          appthrust.io/dns-access: apps-example-com
    records:
      - name:
          pattern: '[a-z0-9]([-a-z0-9]*[a-z0-9])?'
        types:
          - A
```

CRD schema requires `allowedRecordSets[*].namespaces`, `allowedRecordSets[*].namespaces.selector`, `allowedRecordSets[*].records`, `allowedRecordSets[*].records[*].name`, `allowedRecordSets[*].records[*].name.pattern`, and `allowedRecordSets[*].records[*].types`. `records` and `types` must not be empty arrays.

`namespaces.selector` uses the Kubernetes `LabelSelector` shape, but an empty selector is not allowed. The initial design does not provide a policy that opens a shared zone to all namespaces.

The webhook validates that each record name pattern compiles as a regular expression, that each type is supported by core or provider schema, and that the namespace selector is not empty.

`records[].name.pattern` is a regular expression. There is no custom DSL. The webhook validates that patterns compile on `Zone` create/update. Patterns always use full-match semantics, so users do not need to write `^` and `$`. For example, `foo` matches only `foo`, not `foo.bar`. To allow `@`, `*`, or `*.foo`, include them explicitly in the pattern.

The pattern target is only normalized `RecordSet.spec.name`, not an expanded FQDN. Examples: `www`, apex `@`, wildcard `*`, and subdomain wildcard `*.foo`.

`records[].name.pattern: '.*'` is allowed. It is explicit policy granting all record names for the selected namespaces and record types.

`records[].types` is compared with `RecordSet.spec.type`. Provider options do not change this comparison. Route 53 ALIAS is represented as `type=A` or `type=AAAA` plus `spec.options.alias`, so it is allowed by `types: [A]` or `types: [AAAA]`.

Examples:

```yaml
records:
  - name:
      pattern: '[a-z0-9]([-a-z0-9]*[a-z0-9])?'
    types:
      - A
```

Allows only single-label non-apex, non-wildcard A records.

```yaml
records:
  - name:
      pattern: '[a-z0-9]([-a-z0-9]*[a-z0-9])?\.team-a'
    types:
      - A
```

Allows A records one level under `team-a`, such as `foo.team-a`.

```yaml
records:
  - name:
      pattern: '_acme-challenge\.[a-z0-9]([-a-z0-9]*[a-z0-9])?'
    types:
      - TXT
```

Allows TXT records for ACME challenges.

For cross-namespace access, `records` is required. When a shared zone is exposed to application namespaces, namespace-only grants could allow editing apex, wildcard, NS, TXT, or other important records. Therefore record name and type restrictions are written together. `@`, `*`, `*.foo`, `NS`, and `TXT` are not allowed unless explicitly granted by record policy.

`allowedRecordSets` is cross-namespace policy only. If a `RecordSet` references a `Zone` in the same namespace, `allowedRecordSets` is not evaluated. Same namespace is treated as one trust boundary. This allows application engineers to own a custom-domain `Zone` and manage `RecordSet` resources in the same namespace. Shared zones are placed in a platform namespace and limited with `allowedRecordSets`.

Admission validates only the object-local shape of `allowedRecordSets`: required fields, selector syntax, record name pattern syntax, and record type values. It does not reject a `RecordSet` create/update because the current `Zone`, `RecordSet` namespace labels, record name, or record type are outside the policy. The Core ZoneUnit Controller evaluates the saved objects and sets `RecordSet.status.conditions[Accepted]` to `False`, reason `NotAllowedByZone`, when a cross-namespace `RecordSet` is not allowed by its referenced `Zone`.

When updating `Zone.spec.allowedRecordSets`, admission does not list existing `RecordSet` resources and does not reject policy shrink. If the new policy makes existing cross-namespace `RecordSet` resources disallowed, the Core ZoneUnit Controller re-evaluates them and returns `Accepted=False`, reason `NotAllowedByZone`. Same-namespace `RecordSet` resources are not affected by `allowedRecordSets`.

Policy drift alone does not delete external DNS records. Even if webhook downtime or races make an existing `RecordSet` disallowed, controllers return `Accepted=False`, reason `NotAllowedByZone`, and do not proceed with create/update reconciliation. If the `RecordSet` already has an owner item in `ZoneUnit.spec.recordSets[]`, core keeps that item as an ownership and cleanup ledger with `recordSets[].allowed: false`. Provider controllers must not create or update a provider-side record for an item with `allowed: false` while `deletionRequested` is false. External records are deleted only when the owner `RecordSet` is deleted and the matching `ZoneUnit.spec.recordSets[]` item requests deletion.

Namespace label updates are not rejected by dns-api admission. `allowedRecordSets` depends on namespace selectors, so label changes can move existing `RecordSet` resources out of policy. Controllers treat this as policy drift and return `Accepted=False`, reason `NotAllowedByZone`. dns-api does not intercept cluster-scoped `Namespace` updates.

All `allowedRecordSets` rejections use reason `NotAllowedByZone`. Namespace selector mismatch, record name pattern mismatch, and record type mismatch are not separate reasons. `RecordSet.status.conditions[Accepted].message` uses this wording contract:

1. If `RecordSet` and `Zone` are in the same namespace, allow the request and do not evaluate `allowedRecordSets`.
2. If a cross-namespace `RecordSet` has no rule matching its namespace labels, use `RecordSet namespace is not allowed by the referenced Zone.`
3. If a namespace rule matches but no `records[].name.pattern` matches `RecordSet.spec.name`, use `RecordSet name is not allowed by the referenced Zone.`
4. If namespace and record name match but `RecordSet.spec.type` is not included in `records[].types`, use `RecordSet type is not allowed for this RecordSet name by the referenced Zone.`

These messages are coarse troubleshooting categories. They must not include selectors, namespace labels, record name patterns, allowed type lists, or rule indexes.

Initial kest success paths do not cover cross-namespace `allowedRecordSets`. Initial kest uses same-namespace `Zone` and `RecordSet`. Shared-zone policy is covered by envtest or later kest scenarios.

## domainName

`Zone.spec.domainName` is not mutated by the admission webhook. To keep stored values directly comparable, only canonical values are accepted.

Accepted:

- `example.com`: lowercase, no trailing root dot.
- `xn--bcher-kva.example`: IDN as Punycode ASCII.

Rejected:

- `Example.COM.`: uppercase or trailing root dot.
- `bücher.example`: Unicode IDN.

## Zone Status

`Zone.status` contains standard conditions and minimal core API values. Provider-specific structured status is stored as an inline object at `status.provider.data`.

When the Route 53 controller has created or confirmed a hosted zone, `Zone.status.nameServers` contains the Route 53 assigned name servers. Route 53 hosted zone ID is stored in `Zone.status.provider.data`. Caller reference and pending change information are internal provider state and are stored in `ZoneUnit.status.provider.state` when needed.

Example:

```yaml
status:
  nameServers:
    - ns-123.awsdns-45.com
    - ns-678.awsdns-90.net
  provider:
    data:
      hostedZoneID: Z1234567890
```

`Accepted` means the resource is acceptable before calling the provider API: references, namespace policy, and platform policy are valid. Detailed `ZoneClass` problems live on `ZoneClass.status`.

`Accepted=False` reasons:

- `InvalidZoneClassRef`: `zoneClassRef` cannot be resolved or has an invalid reference shape.
- `NotAllowedByZoneClass`: the `Zone` does not match `ZoneClass.spec.allowedZones`.
- `ZoneClassNotAccepted`: the referenced `ZoneClass` is not usable.
- `InvalidIdentityRef`: provider-specific identity reference has invalid shape, or the resolved identity is rejected or not usable.
- `InvalidProvider`: `Zone.spec.provider` cannot be resolved, the Provider version has no `Zone` schema, or the Provider does not match the responsible controller.
- `ProviderMismatch`: `Zone.spec.provider` differs from referenced `ZoneClass.spec.provider`.
- `InvalidProviderPayload`: provider-owned inline payload does not match the selected Provider schema.
- `ProviderConversionFailed`: provider-owned inline payload conversion failed permanently or returned invalid storage payload.
- `InvalidAdoption`: `spec.adoption` does not match the provider schema.
- `ManagedResourceMismatch`: `spec.adoption` points to a different external zone than the one already managed.
- `DeniedByPolicy`: selected `ZoneClass` or provider parameters reject the resource through preflight policy.

`Accepted=Unknown` reasons include unresolved material and temporary conversion failures:

- `IdentityNotResolved`: provider-specific identity object or its accepted state is not resolved enough to decide.
- `ProviderConversionUnavailable`: Provider conversion webhook is temporarily unavailable, timed out, or returned a retryable failure.

`Programmed` means the desired state has been reflected to the provider.

`Programmed=True` does not mean DNS is resolvable. It means the last provider-side state observed by the controller matches the accepted desired state and there is no pending provider change. For Route 53, `Programmed=True` means no `pendingHostedZoneChange` exists in `ZoneUnit.status.provider.state` and the observed hosted zone matches `ZoneUnit.spec.zone` and `ZoneClass.spec.parameters`. Parent-zone NS delegation, public DNS resolvability, and resolver reachability are not guaranteed.

`Programmed=False` reasons:

- `ProviderChangePending`: the provider accepted a change and completion is pending.
- `ProviderIdentityNotReady`: the provider-specific identity has not passed live credential checks.
- `ExternalResourceNotFound`: the resource referenced or derived by `spec.adoption` does not exist.
- `ExternalResourceMismatch`: the resource referenced or derived by `spec.adoption` exists but does not match spec.
- `ProviderConflict`: provider-side conflict such as an existing same-name zone.
- `ProviderAccessDenied`: provider API permission failure.
- `ProviderInvalidRequest`: provider API rejected the request as invalid.
- `ProviderUnavailable`: provider API outage, rate limit, or temporary failure.
- `ReconcileError`: other controller internal error.

Zone status validation accepts only `Accepted` and `Programmed` condition types. `condition.status` must be `True`, `False`, or `Unknown`. `condition.reason` must match the core reason set for the condition type and status, or an entry declared in the selected Provider version `zone.additionalConditionReasons`.

Core Zone condition reason combinations:

- `Accepted=True`: `Accepted`.
- `Accepted=False`: `InvalidZoneClassRef`, `NotAllowedByZoneClass`, `ZoneClassNotAccepted`, `InvalidIdentityRef`, `InvalidProvider`, `ProviderMismatch`, `InvalidProviderPayload`, `ProviderConversionFailed`, `InvalidAdoption`, `ManagedResourceMismatch`, `DeniedByPolicy`.
- `Accepted=Unknown`: `Reconciling`, `ZoneClassNotResolved`, `IdentityNotResolved`, `ProviderStorageVersionNotResolved`, `ProviderConversionUnavailable`, `OwnerStateNotResolved`, `ReconcileError`.
- `Programmed=True`: `Programmed`.
- `Programmed=False`: `ProviderChangePending`, `ProviderIdentityNotReady`, `ExternalResourceNotFound`, `ExternalResourceMismatch`, `ProviderConflict`, `ProviderAccessDenied`, `ProviderInvalidRequest`, `ProviderUnavailable`, `ReconcileError`.
- `Programmed=Unknown`: `Reconciling`, `ProviderChangePending`, `ProviderUnavailable`, `ReconcileError`.

`Zone.status.nameServers` is validated independently of provider data. An empty or omitted list is allowed before provider-assigned name servers are observed. Every non-empty item must be a canonical lowercase ASCII DNS name without a trailing root dot. Unicode, uppercase, empty labels, trailing dots, IP addresses, URLs, and URIs are rejected. Duplicate name servers are rejected after DNS-name normalization.

## Zone Events

Kubernetes Events are not the source of truth for `Zone` state. `Zone.status.conditions` and `Zone.status.provider.data` are the source of truth. Events are diagnostic information for users and UI. Event retention, aggregation, and ordering depend on cluster configuration, so automation and GitOps must not depend on Events.

The Core ZoneUnit Controller normally emits an Event when a `Zone.status.conditions` status or reason changes. It does not emit repeated Events when re-observation yields the same state. During API error retries, it does not repeat Events while the same condition reason continues.

Zone Event contract fields:

- `type`: `Normal` or `Warning`.
- `reason`: for condition-derived Events, the same as the condition reason.
- `message`: short human-readable diagnostic text. It must not contain secrets, credentials, or raw provider SDK error bodies.
- `related`: set only when there is a next Kubernetes resource to inspect. It is omitted when references cannot be resolved or when the external resource has no Kubernetes object.

Core Event reasons do not have provider prefixes. Provider-specific Event reasons start with the provider name to avoid collisions because Kubernetes Event reasons are not namespaced. Route 53-specific reasons start with `Route53`, such as `Route53HostedZoneChangeSubmitted`. `reportingController` identifies the emitter but is not used as the reason namespace.

Machine-processable provider data belongs in `Zone.status.provider.data`, not Events. Provider identity authentication details belong on provider identity status, not Zone Events.

Zone Event messages contain only minimal information needed for next steps: usually `domainName`, condition reason, and relevant resource references. For example, `Accepted` messages include the referenced `ZoneClass`; `ProviderIdentityNotReady` and `ProviderAccessDenied` include provider identity references; deletion and retention messages state that the external zone was deleted or retained.

Example messages:

```text
Zone apps.example.com was accepted by ZoneClass tenant-a-platform/route53-public.
Zone apps.example.com is waiting for Route53Identity tenant-a-platform/route53-dev.
Provider accepted a hosted zone change for apps.example.com and is waiting for completion.
Zone apps.example.com was programmed with hosted zone Z1234567890.
Zone apps.example.com was deleted and the external hosted zone was retained.
```

Common Zone Events:

| type | reason | related | When emitted |
| --- | --- | --- | --- |
| `Normal` | `Accepted` | `ZoneClass` | `Accepted=True`. |
| `Warning` | `InvalidZoneClassRef` | resolved `ZoneClass` if any | `Accepted=False` due to invalid `zoneClassRef`. |
| `Warning` | `NotAllowedByZoneClass` | `ZoneClass` | `Accepted=False` due to `ZoneClass.spec.allowedZones`. |
| `Warning` | `ZoneClassNotAccepted` | `ZoneClass` | `Accepted=False` because the referenced class is not usable. |
| `Warning` | `InvalidIdentityRef` | resolved provider identity if any | provider identity reference is invalid. |
| `Warning` | `InvalidProvider` | resolved `Provider` if any | provider schema or version is invalid. |
| `Warning` | `ProviderMismatch` | `ZoneClass` | `Zone.spec.provider` differs from `ZoneClass.spec.provider`. |
| `Warning` | `InvalidAdoption` | none | adoption payload does not match provider schema. |
| `Warning` | `DeniedByPolicy` | `ZoneClass` | provider or platform policy rejects the Zone. |
| `Warning` | `ManagedResourceMismatch` | none | adoption points to a different external zone than the managed one. |
| `Normal` | `ProviderChangePending` | none | provider accepted a hosted zone change and completion is pending. |
| `Normal` | `Programmed` | none | `Programmed=True`. |
| `Warning` | `ExternalResourceNotFound` | none | adoption target does not exist. |
| `Warning` | `ExternalResourceMismatch` | none | adoption target exists but does not match spec. |
| `Warning` | `ProviderConflict` | none | provider-side same-name or deletion conflict. |
| `Warning` | `ProviderAccessDenied` | provider identity | provider API permission failure. |
| `Warning` | `ProviderInvalidRequest` | `Zone` | provider API rejected the request. |
| `Warning` | `ProviderUnavailable` | none | provider API outage, rate limit, or temporary failure. |
| `Warning` | `ReconcileError` | none | controller internal error. |

Lifecycle helper Events:

| type | reason | related | When emitted |
| --- | --- | --- | --- |
| `Normal` | `ExternalResourceAdopted` | none | external zone referenced by adoption is brought under management. |
| `Normal` | `ExternalResourceDeleted` | none | external zone is deleted during `Zone` deletion. |
| `Normal` | `ExternalResourceRetained` | none | external zone is retained during `Zone` deletion. |

## RecordSet

`RecordSet` is a namespace-scoped resource that declares a DNS record set in a `Zone`.

`RecordSet.spec` fields:

- `zoneRef`: reference to the associated `Zone`. If namespace is omitted, it defaults to the `RecordSet` namespace. Immutable.
- `provider.name`: `Provider.metadata.name`. Required and immutable.
- `provider.version`: `Provider.spec.versions[].name`. Required and immutable.
- `type`: DNS record type. Required and immutable. The initial API defines standard bodies for `A`, `AAAA`, `TXT`, `CNAME`, `MX`, `CAA`, and delegated `NS`. Standard `SRV` bodies are not defined initially.
- `name`: owner name within the zone. Required and immutable.
- `ttl`: TTL seconds. Optional integer in CRD schema. Value range `1..2147483647`. Mutable.
- `a`: standard A record body.
- `aaaa`: standard AAAA record body.
- `txt`: standard TXT record body.
- `cname`: standard CNAME record body.
- `mx`: standard MX record body.
- `caa`: standard CAA record body.
- `ns`: standard delegated NS record body.
- `options`: provider-specific opaque object validated by the referenced Provider version `recordSet.schemas.options`.
- `adoption`: specified when adopting an existing external record set. Omitted for creation. Mutable, with the same managed-resource guard as `Zone`.
- `adoption`: provider-specific opaque object validated by the referenced Provider version `recordSet.schemas.adoption`.

`RecordSet.spec.provider` duplicates `ZoneClass.spec.provider` so GitOps can apply `RecordSet`, `Zone`, and `ZoneClass` in any order while still validating `RecordSet.spec.options` and `RecordSet.spec.adoption`. Admission validates the declared Provider version and provider-specific payloads, but it does not reject a `RecordSet` because the referenced `Zone` or `ZoneClass` is absent or because `RecordSet.spec.provider` differs from `ZoneClass.spec.provider`. The Core ZoneUnit Controller reports those reference and Provider consistency problems later as `Accepted=Unknown` or `Accepted=False`.

Provider version `recordSet.supportedTypes` is the list of `spec.type` values that the provider schema can accept. In `v1alpha1`, every listed type must have a core standard body and core validation. The only supported values are `A`, `AAAA`, `TXT`, `CNAME`, `MX`, `CAA`, and delegated `NS`. Provider admission rejects `supportedTypes` entries such as `SRV`, `SVCB`, `HTTPS`, `DS`, `DNSKEY`, `PTR`, `SSHFP`, `URI`, or any other type without a core standard body. Providers may add constraints for the supported standard types, but they cannot introduce body-less record types through provider-specific `options`.

DNS record identity is `zoneRef`, `type`, and `name`. The Core ZoneUnit Controller evaluates this identity when it builds `ZoneUnit.spec.recordSets[]`. Record name is not repeated inside record bodies. Same `zoneRef`, `type`, and `name` always conflicts. CNAME additionally has same-owner-name exclusivity by default: `type=CNAME` for a `zoneRef` and `name` conflicts with every other type at that same `zoneRef` and `name`, and every non-CNAME type conflicts with an existing CNAME at the same `zoneRef` and `name`. This default composition validation is named `forbid-cname-coexistence` and can be disabled for specific provider-supported type pairs by Provider version `zoneUnit.disableValidations`.

Record name grammar:

```text
recordName   : apexName | wildcardName | relativeName ;
apexName     : "@" ;
wildcardName : "*" | "*." relativeName ;
relativeName : label ("." label)* ;
label        : alnum | alnum labelChar* alnum ;
labelChar    : alnum | "-" ;
alnum        : "a".."z" | "0".."9" ;
```

Record names are relative to `Zone.spec.domainName` and stored without a trailing root dot. The admission webhook rejects uppercase, Unicode, empty labels, and trailing dots even when the referenced `Zone` does not exist yet. It rejects owner names that produce an FQDN longer than DNS limits when the referenced `Zone` can be resolved and combined with the zone domain.

Record name validation is type-dependent and split into intrinsic and reference-dependent checks. Intrinsic checks use only `RecordSet.spec.type`, `RecordSet.spec.name`, and the selected Provider version; they run even when `RecordSet.spec.zoneRef` points to a missing `Zone` for GitOps ordering. Intrinsic checks include canonical lowercase ASCII, label grammar, label length, wildcard placement, type-specific underscore allowance, apex CNAME policy, delegated NS apex rejection, and delegated NS wildcard rejection. Reference-dependent checks, such as full FQDN length with the zone domain and namespace policy, run only when the referenced `Zone` and `ZoneClass` can be resolved.

The grammar above is the normal host-style label grammar. TXT and CNAME records additionally allow underscore-prefixed labels such as `_acme-challenge` and `_0123456789abcdef` for DNS verification workflows. Underscore labels are stored as sent, must be lowercase ASCII, and must not be empty after `_`.

Canonical examples for `Zone.spec.domainName=example.com`:

- `@`: `example.com.`
- `*`: `*.example.com.`
- `*.foo`: `*.foo.example.com.`
- `www`: `www.example.com.`
- `api.v1`: `api.v1.example.com.`

Wildcard labels are always `*` in dns-api canonical record names. Provider-specific escaped representations such as Route 53 `\052` are never exposed in `RecordSet.spec.name`, `ZoneUnit.spec.recordSets[].name`, adoption payloads, or status.

`spec.txt.values` items must be non-empty UTF-8 strings with length `1..4000` octets. Exact duplicate strings in the same array are not allowed. The webhook does not canonicalize case, spaces, quote characters, or escape strings. User strings are stored as-is in spec. Provider controllers convert provider-side TXT representations back to logical TXT values and compare as unordered sets. Route 53 quote, escape, and 255-octet chunk boundaries alone must not be treated as drift.

`spec.a.addresses` accepts IPv4 addresses only. `spec.aaaa.addresses` accepts IPv6 addresses only. `type=A` forbids `spec.aaaa`, `spec.txt`, `spec.cname`, `spec.mx`, `spec.caa`, and `spec.ns`. `type=AAAA` forbids `spec.a`, `spec.txt`, `spec.cname`, `spec.mx`, `spec.caa`, and `spec.ns`. `type=TXT` forbids `spec.a`, `spec.aaaa`, `spec.cname`, `spec.mx`, `spec.caa`, and `spec.ns`. `type=CNAME` forbids `spec.a`, `spec.aaaa`, `spec.txt`, `spec.mx`, `spec.caa`, and `spec.ns`. `type=MX` forbids `spec.a`, `spec.aaaa`, `spec.txt`, `spec.cname`, `spec.caa`, and `spec.ns`. `type=CAA` forbids `spec.a`, `spec.aaaa`, `spec.txt`, `spec.cname`, `spec.mx`, and `spec.ns`. `type=NS` forbids `spec.a`, `spec.aaaa`, `spec.txt`, `spec.cname`, `spec.mx`, and `spec.caa`. Provider-specific options that make standard body fields unnecessary use Provider version `recordSet.disableValidations` to disable required core validations and provider CEL rules to reject conflicting fields.

Address arrays do not allow duplicate IP addresses. Duplicate checks compare parsed IP values, not raw strings. IPv6 forms such as `2001:db8::10` and `2001:0db8:0000::0010` are semantic duplicates.

dns-api does not mutate address strings. `RecordSet.spec.a.addresses` and `RecordSet.spec.aaaa.addresses` store strings exactly as sent by users. The webhook rejects unparsable or semantically duplicate addresses but does not rewrite IPv6 forms. Provider controllers parse desired and observed values and compare canonical IP value sets, not raw strings. Textual representation differences alone are not drift.

`spec.cname.target` is the canonical name returned for a CNAME record. It is a DNS name, not an IP address, URL, URI, or Route 53 ALIAS target. It is stored as lowercase ASCII without a trailing root dot. Punycode labels are accepted. Unicode, uppercase, empty labels, trailing dots, and URL-like strings are rejected. The webhook does not mutate `spec.cname.target`; non-canonical input is rejected. CNAME target labels may use underscore-prefixed labels for verification workflows such as certificate validation. Provider controllers compare observed CNAME target names after normalizing trailing dot differences and provider-specific escaping.

Core standard CNAME forbids `spec.name: "@"` by default because a normal CNAME record cannot be created at the zone apex. This default is named core validation `forbid-cname-apex`. A Provider version may disable `forbid-cname-apex` only when the provider has explicit apex CNAME behavior, such as Cloudflare apex CNAME flattening. Disabling `forbid-cname-apex` does not disable CNAME target validation, required `spec.cname.target`, record name grammar, or ZoneUnit same-owner-name composition validation. To point a zone apex at an AWS resource with Route 53, use Route 53 ALIAS on `type=A` or `type=AAAA`, not standard CNAME.

`spec.mx.records` is a non-empty array of mail exchange entries. Each item has `preference` and `exchange`. `preference` is an integer in range `0..65535`; lower values have higher delivery priority. `exchange` is the mail server DNS name. It is stored as lowercase ASCII without a trailing root dot. Punycode labels are accepted. Unicode, uppercase, empty labels, trailing dots, IP addresses, URLs, and URIs are rejected. The webhook does not mutate `spec.mx.records`; non-canonical input is rejected. Exact duplicate `(preference, exchange)` pairs are not allowed. The same preference with different exchanges is allowed. Provider controllers compare observed MX records as unordered sets of `(preference, exchange)` pairs after normalizing trailing dot differences and provider-specific escaping.

`type=MX` may use `spec.name: "@"`. Publishing MX records at the zone apex is a normal use case. Unlike CNAME, MX does not own the whole owner name by itself and may coexist with non-CNAME record types at the same `Zone` and record name. It only conflicts when a CNAME exists at the same owner name, through the CNAME same-name exclusivity rule.

The core webhook does not check whether `spec.mx.records[].exchange` resolves, has A or AAAA records, is inside the same `Zone`, is managed by dns-api, or is reachable by mail delivery agents. Those are DNS resolution, propagation, or operational checks and are outside the initial core API. `exchange` may point to a DNS name outside the managed zone.

Null MX is supported by using exactly one MX entry with `preference: 0` and `exchange: "."`. This declares that the owner name accepts no email. `exchange: "."` is allowed only for Null MX, must use `preference: 0`, and must not be combined with any other MX entry. Other MX exchanges must not be `"."`.

`spec.caa.records` is a non-empty array of Certification Authority Authorization entries. Each item has `flags`, `tag`, and `value`. `flags` is an integer in range `0..255`. `tag` is a non-empty lowercase ASCII alphanumeric string. The initial validation uses the CAA tag grammar instead of a closed enum so future CAA property tags can be used without changing the core API. Known tags such as `issue`, `issuewild`, and `iodef` are normal values, not the complete allowed set. `value` is a non-empty string. Exact duplicate `(flags, tag, value)` entries are not allowed. The same `tag` with different values is allowed. Provider controllers compare observed CAA records as unordered sets of `(flags, tag, value)` tuples after normalizing provider-specific quoting and escaping.

`type=CAA` may use `spec.name: "@"`. Publishing CAA records at the zone apex is a normal use case. CAA does not own the whole owner name by itself and may coexist with non-CNAME record types at the same `Zone` and record name. It only conflicts when a CNAME exists at the same owner name, through the CNAME same-name exclusivity rule.

The core webhook does not validate CA-specific semantics inside `spec.caa.records[].value`. For example, it does not check whether an `issue` value names a real CA domain, whether account parameters are valid, whether an `iodef` value is a deliverable email address or reachable URL, or whether the configured CA actually honors the policy. Those are CA ecosystem or operational checks and are outside the initial core API.

`spec.ns.nameServers` is a non-empty array of delegated name server DNS names. NS `RecordSet` is only for child zone delegation. It is not the authoritative name server set assigned to the current `Zone` itself. Assigned hosted zone name servers remain `Zone.status.nameServers`. Each delegated name server is stored as lowercase ASCII without a trailing root dot. Punycode labels are accepted. Unicode, uppercase, empty labels, trailing dots, IP addresses, URLs, and URIs are rejected. Exact duplicate name servers are not allowed. Provider controllers compare observed delegated NS records as unordered sets after normalizing trailing dot differences and provider-specific escaping.

`type=NS` must not use `spec.name: "@"`. Apex NS records are provider-managed zone records in the initial API and are not managed through `RecordSet`. Use `Zone.status.nameServers` to observe the provider-assigned apex name servers for the current `Zone`. `type=NS` also must not use wildcard owner names such as `*` or `*.foo`; delegated NS is for explicit child owner names such as `team-a` or `dev.platform`.

The core webhook does not check whether `spec.ns.nameServers[]` resolves, is authoritative for the delegated child zone, has glue records, is inside the same `Zone`, is managed by dns-api, or is reachable by resolvers. Those are DNS delegation, propagation, or operational checks and are outside the initial core API. Delegated name servers may point to DNS names outside the managed zone.

Core API does not define provider-specific `spec.options` fields for CNAME, MX, CAA, or delegated NS. `spec.options` remains provider-defined and is validated only by the referenced Provider version schema and CEL rules. Core validation must not name or interpret Route 53-specific fields such as `spec.options.alias`.

The initial API does not provide standard body fields for types other than A, AAAA, TXT, CNAME, MX, CAA, and delegated NS. Headlamp plugin, manual, sample manifests, and Provider manifests must not present or declare standard `SRV` as a creation target until field names, validation, examples, and provider schema support levels are added to this design.

### Controller Responsibility

`RecordSet` is a claim reconciled by the Core ZoneUnit Controller. The core validating webhook validates object-local fields and provider payloads. It does not require the referenced `Zone` or `ZoneClass` to exist before saving the object.

The Core ZoneUnit Controller resolves the referenced `Zone`, `ZoneClass`, and `Provider`, applies `Zone.spec.allowedRecordSets`, evaluates DNS ownership and compatibility rules, converts provider payload to Provider storage version, and writes accepted items to `ZoneUnit.spec.recordSets[]`. If a previously accepted `RecordSet` becomes disallowed by `Zone.spec.allowedRecordSets`, core keeps the existing `ZoneUnit.spec.recordSets[]` item with `allowed: false` so ownership and cleanup state are not lost. Rejected or unresolved claims are reported directly on `RecordSet.status.conditions[Accepted]`. Provider controllers do not read `RecordSet` claims; they read `ZoneUnit.spec.recordSets[]` and write `ZoneUnit.status.recordSets[]`. The Core ZoneUnit Controller projects those provider results back to `RecordSet.status`.

Standard A example:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: RecordSet
metadata:
  namespace: tenant-a-app
  name: www
spec:
  zoneRef:
    name: example-com
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

Cloudflare `proxied`, `comment`, `tags`, and automatic TTL are provider options. Automatic TTL is not represented by the core TTL type; it lives in Cloudflare provider schema `spec.options.ttl`. `proxied` is supported only for A, AAAA, and CNAME. Cloudflare fixed TTL accepts only `60..86400` in `v1alpha1` through Provider `recordSet.validationRules`; `spec.ttl: 1` is not the automatic TTL form for Cloudflare.

Cloudflare A example:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: RecordSet
metadata:
  namespace: tenant-a-app
  name: www
spec:
  zoneRef:
    name: example-com
  provider:
    name: cloudflare.dns.appthrust.io
    version: v1alpha1
  type: A
  name: www
  options:
    ttl: Auto
    proxied: true
    comment: app endpoint
    tags:
      - app:frontend
  a:
    addresses:
      - 192.0.2.10
```

Cloudflare provider schema disables `require-ttl` only when `spec.options.ttl=Auto` exists and uses CEL validation to reject simultaneous `spec.ttl` and `spec.options.ttl`. When `spec.options.proxied=true`, Cloudflare provider validation requires automatic TTL. The controller maps `spec.options.ttl=Auto` to Cloudflare API `ttl=1`; users must not express automatic TTL as core `spec.ttl: 1`.

Standard AAAA example:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: RecordSet
metadata:
  namespace: tenant-a-app
  name: www-v6
spec:
  zoneRef:
    name: example-com
  provider:
    name: route53.dns.appthrust.io
    version: v1alpha1
  type: AAAA
  name: www
  ttl: 300
  aaaa:
    addresses:
      - 2001:db8::10
```

Standard TXT example:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: RecordSet
metadata:
  namespace: tenant-a-app
  name: acme-challenge
spec:
  zoneRef:
    name: example-com
  provider:
    name: route53.dns.appthrust.io
    version: v1alpha1
  type: TXT
  name: _acme-challenge
  ttl: 300
  txt:
    values:
      - challenge-token
      - v=spf1 include:_spf.example.net ~all
```

Standard CNAME example:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: RecordSet
metadata:
  namespace: tenant-a-app
  name: www-cname
spec:
  zoneRef:
    name: example-com
  provider:
    name: route53.dns.appthrust.io
    version: v1alpha1
  type: CNAME
  name: www
  ttl: 300
  cname:
    target: app.example.net
```

Standard MX example:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: RecordSet
metadata:
  namespace: tenant-a-app
  name: mail
spec:
  zoneRef:
    name: example-com
  provider:
    name: route53.dns.appthrust.io
    version: v1alpha1
  type: MX
  name: "@"
  ttl: 300
  mx:
    records:
      - preference: 10
        exchange: mail1.example.net
      - preference: 20
        exchange: mail2.example.net
```

Null MX example:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: RecordSet
metadata:
  namespace: tenant-a-app
  name: no-mail
spec:
  zoneRef:
    name: example-com
  provider:
    name: route53.dns.appthrust.io
    version: v1alpha1
  type: MX
  name: "@"
  ttl: 300
  mx:
    records:
      - preference: 0
        exchange: "."
```

Standard CAA example:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: RecordSet
metadata:
  namespace: tenant-a-app
  name: ca-policy
spec:
  zoneRef:
    name: example-com
  provider:
    name: route53.dns.appthrust.io
    version: v1alpha1
  type: CAA
  name: "@"
  ttl: 300
  caa:
    records:
      - flags: 0
        tag: issue
        value: letsencrypt.org
      - flags: 0
        tag: issuewild
        value: ";"
      - flags: 0
        tag: iodef
        value: mailto:security@example.com
```

Delegated NS example:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: RecordSet
metadata:
  namespace: tenant-a-app
  name: team-a-delegation
spec:
  zoneRef:
    name: example-com
  provider:
    name: route53.dns.appthrust.io
    version: v1alpha1
  type: NS
  name: team-a
  ttl: 300
  ns:
    nameServers:
      - ns-111.example-dns.net
      - ns-222.example-dns.net
```

## ZoneUnit

`ZoneUnit` is the zone-scoped desired-state and ownership ledger built from accepted `Zone` and `RecordSet` claims. It is namespace-scoped and uses the same namespace and name as the owning `Zone`. Users normally create `Zone` and `RecordSet` claims; the Core ZoneUnit Controller creates and updates `ZoneUnit`.

`ZoneUnit.metadata.name` must match `spec.zone.ref.name`; the CRD validates this with CEL. `ZoneUnit.metadata.namespace` must match `spec.zone.ref.namespace`; Kubernetes CRD CEL does not expose `metadata.namespace`, so the core validating webhook rejects namespace mismatches on create and update. Provider controllers do not define an `InvalidZoneUnitName` condition reason and do not handle mismatched names defensively, because invalid `ZoneUnit` names are rejected before provider reconciliation.

`ZoneUnit.spec` is the provider-controller input. The Core ZoneUnit Controller writes it after resolving references, applying core policy, evaluating provider contract rules, converting provider payloads to the Provider storage version, and resolving record ownership conflicts. Provider controllers read `ZoneUnit.spec` and do not read `Zone` or `RecordSet` claims.

`ZoneUnit.spec` fields:

- `provider.name`: Provider resource name, such as `route53.dns.appthrust.io`. Required and immutable.
- `provider.version`: Provider storage version used inside the `ZoneUnit`. Required and immutable.
- `zone`: accepted desired state for the owning `Zone`.
- `zone.ref`: namespace/name reference to the owning `Zone`.
- `zone.observedGeneration`: `Zone.metadata.generation` used to build this item.
- `zone.domainName`: accepted zone domain name.
- `zone.zoneClassRef`: resolved `ZoneClass` reference.
- `zone.adoption`: provider-specific adoption payload after storage-version conversion.
- `recordSets`: accepted desired state for owner `RecordSet` claims.
- `recordSets[].recordSetNamespace`: owner `RecordSet` namespace. This is part of the list-map key.
- `recordSets[].recordSetName`: owner `RecordSet` name. This is part of the list-map key.
- `recordSets[].observedGeneration`: `RecordSet.metadata.generation` used to build this item.
- `recordSets[].name`: `RecordSet.spec.name` as a zone-relative DNS name, not an FQDN.
- `recordSets[].type`: `RecordSet.spec.type`.
- `recordSets[].ttl`, body fields, `options`, and `adoption`: provider-storage desired state used for active reconciliation and retained cleanup.
- `recordSets[].allowed`: omitted or `true` when the item is allowed by current composition policy. `false` means the item is retained only as an ownership and cleanup ledger after `Zone.spec.allowedRecordSets` changed. Provider controllers must not create or update provider-side records for `allowed: false` items unless `deletionRequested` is true.
- `recordSets[].deletionRequested`: set by core when the owner `RecordSet` is deleting and provider-side cleanup is still needed.

`recordSets` is a Kubernetes `listType=map` keyed by scalar fields `recordSetNamespace` and `recordSetName`. `recordSetRef` is not used inside the list item because nested-object list-map keys are not a CRD-safe shape across Kubernetes versions. UID is not a key because provider controllers do not read `RecordSet` claims and therefore cannot rely on `RecordSet.metadata.uid`. When a `RecordSet` has not yet been restored during GitOps recovery, no new item is created for it. If an item already exists in a pre-applied `ZoneUnit`, core preserves it until the matching claim returns or provider cleanup completes.

`ZoneUnit.spec.zone` does not contain `Zone.metadata.uid`. Kubernetes assigns `metadata.uid`, so a `ZoneUnit` pre-applied for GitOps recovery cannot know the UID of a `Zone` claim that will be restored later. Provider controllers must not require the `Zone` UID as desired input.

DNS record identity is still `zone`, `type`, and zone-relative `name`. Same `type` and zone-relative `name` always conflicts. Core also applies composition validation `forbid-cname-coexistence` by default: a `type=CNAME` item owns the whole record name and cannot coexist with any other record type at the same `Zone` and name, and a non-CNAME item cannot coexist with a CNAME item at the same `Zone` and name. A Provider storage version may disable this validation for specific `self` / `other` record-set item pairs through `zoneUnit.disableValidations[]` when the provider intentionally supports that owner-name combination. Route 53 keeps the default CNAME exclusivity. Cloudflare disables `forbid-cname-coexistence` for `CNAME` with `TXT`, `MX`, and `CAA`, and keeps conflicts for `CNAME` with `A` or `AAAA`.

Provider storage versions may add stricter owner-name composition checks through `zoneUnit.validationRules[]`. Cloudflare uses a ZoneUnit validation rule to reject `NS` coexisting with any other record type at the same owner name because the Cloudflare DNS Records API documents that restriction.

If multiple `RecordSet` claims target the same record identity, core keeps the existing owner in `ZoneUnit.spec.recordSets[]` when one exists, including an owner retained with `allowed: false`. Otherwise, it chooses a deterministic owner by creation timestamp, then namespace/name. Rejected claims get `Accepted=False`, reason `RecordSetConflict`, on their own `RecordSet.status`. Provider controllers do not receive newly rejected claims, but they may receive retained cleanup-ledger items with `allowed: false`.

`ZoneUnit.status` is the provider-controller output:

- `observedGeneration`: `ZoneUnit.metadata.generation` observed by the provider controller.
- `conditions`: resource-level conditions. Initially this contains `Programmed`. It does not contain a resource-level `Accepted`; acceptance is reported per claim target.
- `provider.state`: provider-controller internal state for the whole unit, such as Route 53 pending hosted-zone and record-set change IDs. It is not projected to claim status.
- `zone.conditions`: provider acceptance and programming result for the `Zone` claim.
- `zone.nameServers`: provider-assigned name servers to project to `Zone.status.nameServers`.
- `zone.provider.data`: public provider data to project to `Zone.status.provider.data`.
- `zone.provider.state`: optional provider-controller internal state for the zone target.
- `recordSets`: provider result for accepted owner `RecordSet` claims.
- `recordSets[].recordSetNamespace`: owner `RecordSet` namespace matching `spec.recordSets[]`.
- `recordSets[].recordSetName`: owner `RecordSet` name matching `spec.recordSets[]`.
- `recordSets[].observedGeneration`: owner `RecordSet` generation observed in `ZoneUnit.spec`.
- `recordSets[].conditions`: provider acceptance and programming result for that `RecordSet` claim.
- `recordSets[].provider.data`: public provider data to project to `RecordSet.status.provider.data`.
- `recordSets[].provider.state`: optional provider-controller internal state for that record target.

Provider controllers write `ZoneUnit.status` only. The Core ZoneUnit Controller projects `ZoneUnit.status.zone` to `Zone.status` and `ZoneUnit.status.recordSets[]` to each owner `RecordSet.status`. Claim status has `status.provider.data` for public provider data, but it does not expose `provider.state`.

Provider-specific status follows a public-data boundary:

- `provider.data` contains only provider-specific values that help the user operate the resource outside dns-api. Examples include external IDs that can be copied into adoption settings, used to open a provider dashboard, or used in support and troubleshooting workflows.
- `provider.state` contains provider-controller internal state and observed details that are needed for reconciliation but do not help the user take direct action. It is not a cache for exposing provider API responses.
- Values already represented in core `spec`, core `status`, or provider identity status are not duplicated in `provider.data`.

`ZoneUnit.status.conditions[Programmed]` is `True` only when the zone target and every accepted record-set target are `Programmed=True`. It is `Unknown` if any target is `Programmed=Unknown` and none is `False`. It is `False` if any target is `Programmed=False`.

`False` takes precedence over `Unknown` because the resource-level `Programmed` condition is also the operational summary for alerting and UI lists. If one target has a known provider error, conflict, or drift, the `ZoneUnit` as a whole is not programmed even when another target is still being observed. Unknown targets remain visible on their own target conditions, but they must not hide an already known failure from the aggregate condition.

Deletion uses both core and provider finalizers on `ZoneUnit`:

- Core keeps a finalizer on `Zone` and `ZoneUnit` while the external cleanup result must be observed.
- Provider controllers keep their own finalizer on `ZoneUnit` while provider cleanup is in progress.
- `RecordSet` is a restrict-style child of `Zone`. While any `RecordSet` still references a deleting `Zone`, core keeps the matching `ZoneUnit` active and does not send the provider cleanup signal. The deleting `Zone` reports `Programmed=False`, reason `ProviderChangePending`, with a message that the remaining `RecordSet` resources must be deleted or moved first.
- If a `RecordSet` that references a deleting `Zone` is itself deleting, core keeps or writes its `ZoneUnit.spec.recordSets[]` item with `deletionRequested=true` until provider record-set cleanup completes.
- After no `RecordSet` references the deleting `Zone`, core deletes the matching `ZoneUnit` before removing the `Zone` finalizer.
- If the deleting `Zone` has no remaining `RecordSet` references and the matching `ZoneUnit` does not exist, core removes the `Zone` finalizer because there is no provider-owned cleanup state to observe.
- `ZoneUnit.metadata.deletionTimestamp` is the provider cleanup signal. Provider controllers delete or retain external resources according to provider policy, update `ZoneUnit.status`, and remove their provider finalizer after cleanup completes.
- Core removes its `ZoneUnit` finalizer after provider cleanup is complete, then removes the `Zone` finalizer.
- Deleting a `RecordSet` sets `recordSets[].deletionRequested=true`. Provider controllers delete the external record target, then report completion. Core removes the `RecordSet` finalizer and later removes the item from `ZoneUnit.spec.recordSets[]`.

The validating webhook rejects manual `ZoneUnit` deletion while the owning `Zone` exists and is not deleting. The owning `Zone` is resolved from `ZoneUnit.spec.zone.ref`. This prevents a user or GitOps controller from sending the provider cleanup signal while the desired `Zone` claim is still active.

`ZoneUnit` deletion is allowed when the owning `Zone` exists and has `metadata.deletionTimestamp`; this is the normal path started by the Core ZoneUnit Controller during `Zone` deletion. `ZoneUnit` deletion is also allowed when the owning `Zone` is missing, so operators can remove a pre-applied or orphaned GitOps recovery ledger. Core does not delete a `ZoneUnit` merely because the owning `Zone` is temporarily missing during GitOps restore.

Provider controllers perform provider cleanup only for `ZoneUnit` resources that are in their scope and have their provider finalizer or otherwise have provider-owned cleanup to complete. They do not add claim finalizers and do not remove core finalizers. If a deleting `ZoneUnit` has no provider finalizer and no provider-owned cleanup state, provider controllers may ignore it.

`ZoneUnit` is not normally GitOps-managed, but it may be pre-applied for disaster recovery before restoring `Zone` and `RecordSet` claims. This allows ownership decisions and unresolved conflict results to survive a cluster rebuild. While the `Zone` is missing, core does not rebuild `ZoneUnit.spec`; it returns `Accepted=Unknown`, reason `ZoneNotResolved`, to matching `RecordSet` claims that already exist.

If a pre-applied `ZoneUnit` contains record-set ownership state whose owner claim or matching status material is not resolved yet, core keeps the `ZoneUnit.spec.recordSets[]` item and reports `Accepted=Unknown`, reason `OwnerStateNotResolved`, where a claim status can be written. dns-api does not use the proposal-only reasons `OwnerResolutionPending` or `RecordSetRefNotResolved`; the problem is unresolved ownership state during restore, not a separate public reference kind.

## RecordSet Status

`RecordSet` belongs to a single `Zone`, so status does not contain an array of parents. Acceptance and programming results are returned in `RecordSet.status.conditions`.

`RecordSet.status` fields:

- `observedGeneration`: `metadata.generation` at the time status was computed.
- `zone`: the actual associated `Zone`.
- `zone.ref`: namespace/name reference to the actual associated `Zone` resolved by the Core ZoneUnit Controller.
- `provider.data`: public provider data projected from `ZoneUnit.status.recordSets[].provider.data`, validated by Provider version `recordSet.schemas.statusProviderData`.
- `conditions`: standard conditions.

`RecordSet.status.zone.ref` is status output for the controller-resolved parent `Zone`. It is not merely a copy of the user-written `spec.zoneRef` object. When the referenced `Zone` has been resolved, `status.zone.ref.namespace` and `status.zone.ref.name` match `spec.zoneRef` after applying the default namespace. This lets users, UI, and documentation show the actual parent relation without reimplementing defaulting or resolution. If the `Zone` cannot be resolved, `status.zone` may be omitted while `Accepted=Unknown`, reason `ZoneNotResolved`, is reported.

`RecordSet.status.zone` contains only resolved `Zone` identity. `RecordSet.status.zone.controllerName` is not part of the API and must not be present in CRD schemas, Go API types, generated clients, examples, status projection, UI status reads, or manual documentation. Provider-controller ownership remains discoverable through the resolved `Zone`, its `ZoneClass`, and `ZoneClass.spec.controllerName`; it is not duplicated on `RecordSet.status`.

`Accepted` is the composed result of core composition acceptance and provider acceptance. It is `Unknown` while the controller cannot resolve enough input to decide, such as a missing `Zone`, missing `ZoneClass`, unavailable Provider storage version, or missing owner state in a pre-applied `ZoneUnit`. If the referenced `Zone` is resolved but `Zone.status.conditions[Accepted]` is `False`, the Core ZoneUnit Controller reports the child `RecordSet` as `Accepted=False`, reason `ZoneNotAccepted`; the `RecordSet` does not keep an older accepted status from before the parent `Zone` became unusable.

`Accepted=False` reasons:

- `InvalidZoneRef`: `zoneRef` cannot be resolved or has an invalid reference shape.
- `InvalidProvider`: `provider` cannot be resolved, the Provider version has no `RecordSet` schema, or the Provider does not support `RecordSet.spec.type`.
- `ProviderMismatch`: `RecordSet.spec.provider` differs from referenced `ZoneClass.spec.provider`.
- `InvalidIdentityRef`: provider-specific identity reference has invalid shape, or the resolved identity is rejected or not usable.
- `InvalidProviderPayload`: provider-owned inline payload does not match the selected Provider schema.
- `ProviderConversionFailed`: provider-owned inline payload conversion failed permanently or returned invalid storage payload.
- `NotAllowedByZone`: does not match `Zone.spec.allowedRecordSets`.
- `ZoneNotAccepted`: referenced `Zone` is not usable.
- `RecordSetConflict`: the same record identity is owned by another `RecordSet` item in `ZoneUnit.spec.recordSets[]`.
- `InvalidAdoption`: `spec.adoption` does not match provider schema.
- `ManagedResourceMismatch`: adoption points to a different external record set than the one already managed.
- `DeniedByPolicy`: selected `Zone` or provider schema preflight policy rejects the resource.

`Accepted=Unknown` reasons include unresolved material and temporary conversion failures:

- `IdentityNotResolved`: provider-specific identity object or its accepted state is not resolved enough to decide.
- `ProviderConversionUnavailable`: Provider conversion webhook is temporarily unavailable, timed out, or returned a retryable failure.

`Programmed` means the desired state has been reflected to the provider. Provider-specific completion criteria are defined in provider sections. For Route 53, `Programmed=True` means the last observed RRSet matches the `ZoneUnit.spec.recordSets[]` item and there is no `pendingRecordSetChange` affecting that record identity.

`Programmed=False` reasons:

- `ProviderChangePending`: provider accepted a change and completion is pending.
- `ProviderIdentityNotReady`: the provider-specific identity has not passed live credential checks.
- `ProviderChangeDeferred`: this reconcile did not submit the change because of provider API batch limits or serialization; it will run in a later reconcile.
- `ExternalResourceNotFound`: adoption target external record set does not exist.
- `ExternalResourceMismatch`: adoption target external record set exists but does not match spec.
- `ProviderConflict`: provider-side record set conflict.
- `ProviderAccessDenied`: provider API permission failure.
- `ProviderInvalidRequest`: provider API rejected the request as invalid.
- `ProviderUnavailable`: provider API outage, rate limit, or temporary failure.
- `ReconcileError`: other controller internal error.

RecordSet status validation accepts only `Accepted` and `Programmed` condition types. `condition.status` must be `True`, `False`, or `Unknown`. `condition.reason` must match the core reason set for the condition type and status, or an entry declared in the selected Provider version `recordSet.additionalConditionReasons`.

Core RecordSet condition reason combinations:

- `Accepted=True`: `Accepted`.
- `Accepted=False`: `InvalidZoneRef`, `InvalidProvider`, `ProviderMismatch`, `InvalidIdentityRef`, `InvalidProviderPayload`, `ProviderConversionFailed`, `NotAllowedByZone`, `ZoneNotAccepted`, `RecordSetConflict`, `InvalidAdoption`, `ManagedResourceMismatch`, `DeniedByPolicy`.
- `Accepted=Unknown`: `Reconciling`, `ZoneNotResolved`, `ZoneClassNotResolved`, `IdentityNotResolved`, `ProviderStorageVersionNotResolved`, `ProviderConversionUnavailable`, `OwnerStateNotResolved`, `ReconcileError`.
- `Programmed=True`: `Programmed`.
- `Programmed=False`: `ProviderChangePending`, `ProviderIdentityNotReady`, `ProviderChangeDeferred`, `ExternalResourceNotFound`, `ExternalResourceMismatch`, `ProviderConflict`, `ProviderAccessDenied`, `ProviderInvalidRequest`, `ProviderUnavailable`, `ReconcileError`.
- `Programmed=Unknown`: `Reconciling`, `ProviderChangePending`, `ProviderChangeDeferred`, `ProviderUnavailable`, `ReconcileError`.

`RecordSetConflict` means the same `Zone`, record type, and record name is owned by another `RecordSet` item in `ZoneUnit.spec.recordSets[]`. Provider controllers do not receive rejected `RecordSet` claims.

## RecordSet Events

Kubernetes Events are not the source of truth for `RecordSet` state. `RecordSet.status.conditions` and `RecordSet.status.provider.data` are the source of truth. Events are diagnostic information for users and UI. The Core ZoneUnit Controller normally emits an Event when a condition status or reason changes; it does not repeatedly emit Events for unchanged re-observations.

RecordSet Events use the same four fields as Zone Events: `type`, `reason`, `message`, and `related`. For condition-derived Events, `reason` equals the condition reason. Core Event reasons do not include provider prefixes. Provider-specific reasons start with the provider name.

RecordSet Event messages include the minimum needed context: `zoneRef`, record name, record type, FQDN, and condition reason. Conflict messages include the owner `RecordSet` when available. Provider API rejection messages may include a sanitized provider error summary when the provider returns a structured error code and message. Provider identity details, secrets, credentials, request headers, raw request bodies, raw response bodies, and raw provider SDK error bodies are not included.

Example messages:

```text
RecordSet type=A name=www in Zone app/example-com was accepted.
RecordSet type=A name=www is not allowed by Zone app/example-com.
RecordSet type=A name=www is already allocated to RecordSet team-a/www.
Provider accepted a change for RecordSet type=A name=www and is waiting for completion.
RecordSet type=A name=www was programmed in Zone app/example-com.
```

Common RecordSet Events:

| type | reason | related | When emitted |
| --- | --- | --- | --- |
| `Normal` | `Accepted` | `Zone` | `Accepted=True`. |
| `Warning` | `InvalidZoneRef` | resolved `Zone` if any | invalid `zoneRef`. |
| `Warning` | `InvalidProvider` | resolved `Provider` if any | provider schema or version is invalid. |
| `Warning` | `ProviderMismatch` | `Zone` | `RecordSet.spec.provider` differs from `ZoneClass.spec.provider`. |
| `Warning` | `NotAllowedByZone` | `Zone` | not allowed by `Zone.spec.allowedRecordSets`. |
| `Warning` | `ZoneNotAccepted` | `Zone` | referenced `Zone` is not usable. |
| `Warning` | `RecordSetConflict` | `RecordSet` | record identity is owned by another `RecordSet`. |
| `Warning` | `InvalidAdoption` | none | adoption payload does not match provider schema. |
| `Warning` | `DeniedByPolicy` | `Zone` or `Provider` | provider or platform policy rejects the resource. |
| `Warning` | `ManagedResourceMismatch` | none | adoption points to a different external record set than the managed one. |
| `Normal` | `ProviderChangePending` | `Zone` | provider accepted a change and completion is pending. |
| `Normal` | `ProviderChangeDeferred` | `Zone` | batch limit or serialization deferred the change. |
| `Normal` | `Programmed` | `Zone` | `Programmed=True`. |
| `Warning` | `ExternalResourceNotFound` | none | adoption target does not exist. |
| `Warning` | `ExternalResourceMismatch` | none | adoption target exists but does not match spec. |
| `Warning` | `ProviderConflict` | `Zone` | provider-side conflict. |
| `Warning` | `ProviderAccessDenied` | provider identity | provider API permission failure. |
| `Warning` | `ProviderInvalidRequest` | `Zone` | provider API rejected the request. |
| `Warning` | `ProviderUnavailable` | `Zone` | provider API outage, rate limit, or temporary failure. |
| `Warning` | `ReconcileError` | none | controller internal error. |

RecordSet lifecycle helper Events:

| type | reason | related | When emitted |
| --- | --- | --- | --- |
| `Normal` | `ExternalResourceAdopted` | `Zone` | external record set referenced by adoption is brought under management. |
| `Normal` | `ExternalResourceDeleted` | `Zone` | external record set is deleted during `RecordSet` deletion. |
