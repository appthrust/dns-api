# Cloudflare Provider

## CloudflareIdentity

`CloudflareIdentity` is a namespace-scoped provider-specific resource. It represents a Cloudflare API token used by the Cloudflare controller. `CloudflareIdentity` lives in the same namespace as the `ZoneClass` that references it.

`CloudflareIdentity.spec` does not contain an account ID. A Cloudflare API token is already scoped by Cloudflare, and duplicating the account ID in spec would create two sources of desired identity. The controller observes the account visible through the token and writes that observed account to status.

`CloudflareIdentity.spec` fields:

- `accessToken`: Cloudflare API token source. Required.
- `accessToken.secretRef`: reference to a Kubernetes Secret in the same namespace as the `CloudflareIdentity`. Required.
- `accessToken.secretRef.name`: Secret name. Required.
- `accessToken.secretRef.key`: Secret key containing the raw Cloudflare API token. Required.

The referenced Secret value is the raw API token string used as a Bearer token. The controller must not store the raw token in status, Events, logs, metrics labels, or error messages.

`CloudflareIdentity.status` contains:

- `observedGeneration`.
- `account`: the single Cloudflare account observed through the token.
- `account.id`: Cloudflare account ID.
- `account.name`: Cloudflare account display name returned by Cloudflare.
- `account.type`: Cloudflare account type when returned by Cloudflare.
- `accessToken`: non-secret token metadata observed through token verification.
- `accessToken.id`: Cloudflare token identifier returned by token verification.
- `accessToken.status`: token status returned by Cloudflare, such as `active`, `disabled`, or `expired`.
- `accessToken.expiresOn`: token expiration time when returned by Cloudflare.
- `accessToken.notBefore`: token not-before time when returned by Cloudflare.
- `conditions`: `Accepted` and `Ready`.
- `lastCredentialCheckTime`: last live token and account check time.
- `nextCredentialCheckTime`: approximate next scheduled live check time. Empty means no automatic retry is scheduled.

`Accepted=True` means the spec can be statically accepted: Secret reference shape and platform policy are valid. `Ready=True` means the controller has read the token Secret, verified the token with Cloudflare, listed accounts visible through the token, and found exactly one account.

The controller calls Cloudflare `GET /user/tokens/verify` to verify that the token is usable and to observe token metadata. It calls Cloudflare `GET /accounts` to discover the account visible through the token. `GET /accounts` returns account objects including `id`, `name`, and `type`, so `status.account.name` is expected when Cloudflare returns it.

If `GET /accounts` returns exactly one account, the controller stores that account in `status.account`. If it returns zero accounts, the identity is not ready. If it returns more than one account, the initial Cloudflare provider treats the token as ambiguous and is not ready.

The initial Cloudflare provider requires tokens to include account-level read permission for exactly one account, plus zone and DNS edit permission for zones in that same account. In Cloudflare dashboard terms, the token uses one included account, not all accounts. It includes an account read permission such as `Account Settings:Read`, zone resources scoped to all zones from that account, and zone permissions `Zone:Edit` and `DNS:Edit`. A token that has `Zone:Edit` and `DNS:Edit` but no account read permission can be active and still fail `GET /accounts`; such a token is not `Ready=True` in dns-api because the controller cannot observe the account identity safely.

`Accepted=False` reasons:

- `InvalidSecretRef`: `spec.accessToken.secretRef` is missing or invalid.
- `DeniedByPolicy`: platform policy rejects this identity.

`Ready=False` reasons:

- `SecretNotFound`: referenced Secret does not exist.
- `AccessTokenUnavailable`: referenced Secret key does not exist or is empty.
- `AccessTokenInvalid`: Cloudflare token verification fails because the token is invalid.
- `AccessTokenInactive`: Cloudflare token verification succeeds but status is not `active`.
- `CloudflareAccountNotFound`: token verification succeeds but no Cloudflare account is visible through the token.
- `CloudflareAccountAmbiguous`: more than one Cloudflare account is visible through the token.
- `ProviderAccessDenied`: Cloudflare API permission failure.
- `ProviderUnavailable`: Cloudflare API outage, rate limit, or temporary failure.
- `ReconcileError`: other controller internal error.

`lastCredentialCheckTime` is set whenever a live token and account check finishes, whether success or failure. `nextCredentialCheckTime` is set when the controller schedules a retry. It is diagnostic and approximate; Kubernetes watch events, Secret updates, controller restart, or controller-runtime resync may reconcile earlier.

Example:

```yaml
apiVersion: cloudflare.dns.appthrust.io/v1alpha1
kind: CloudflareIdentity
metadata:
  namespace: tenant-a-platform
  name: cloudflare-ci
spec:
  accessToken:
    secretRef:
      name: cloudflare-ci-api-token
      key: api-token
```

Example status:

```yaml
status:
  observedGeneration: 1
  account:
    id: 023e105f4ecef8ad9ca31a8372d0c354
    name: Appthrust-dns-api-ci@craftsman-software.com's Account
    type: standard
  accessToken:
    id: 0123456789abcdef0123456789abcdef
    status: active
  conditions:
    - type: Accepted
      status: "True"
      reason: Accepted
      observedGeneration: 1
    - type: Ready
      status: "True"
      reason: Ready
      observedGeneration: 1
```

### CloudflareIdentity Events

CloudflareIdentity Events are diagnostic. They do not replace status.

Common Events:

| type | reason | related | When emitted |
| --- | --- | --- | --- |
| `Normal` | `Accepted` | none | `Accepted=True`. |
| `Warning` | `InvalidSecretRef` | none | Secret reference validation fails. |
| `Warning` | `DeniedByPolicy` | none | platform policy rejects the identity. |
| `Normal` | `Ready` | none | live token and account check succeeds. |
| `Warning` | `SecretNotFound` | none | referenced Secret does not exist. |
| `Warning` | `AccessTokenUnavailable` | none | referenced Secret key does not exist or is empty. |
| `Warning` | `AccessTokenInvalid` | none | Cloudflare token verification fails because the token is invalid. |
| `Warning` | `AccessTokenInactive` | none | Cloudflare token verification succeeds but status is not `active`. |
| `Warning` | `CloudflareAccountNotFound` | none | no Cloudflare account is visible through the token. |
| `Warning` | `CloudflareAccountAmbiguous` | none | more than one Cloudflare account is visible through the token. |
| `Warning` | `ProviderAccessDenied` | none | Cloudflare API permission failure. |
| `Warning` | `ProviderUnavailable` | none | Cloudflare API outage, rate limit, or temporary failure. |
| `Warning` | `ReconcileError` | none | controller internal error. |

### Controller Responsibility

Cloudflare controller reconciles `CloudflareIdentity`. It validates the spec, reads the token from the referenced Secret, calls Cloudflare `GET /user/tokens/verify`, calls Cloudflare `GET /accounts`, updates status, and emits Events. It never stores credential material in status or Events.

`CloudflareIdentity` reconciliation does not create, update, or delete Cloudflare zones or DNS records. Zone reconciliation and RecordSet reconciliation are owned by the Cloudflare controller paths for those core resources.

## Cloudflare Provider Schema

Cloudflare provider schema is represented by `Provider/cloudflare.dns.appthrust.io` version `v1alpha1`. Cloudflare `ZoneClass` configuration is stored in `ZoneClass.spec.parameters`. `ZoneClass.spec.provider` and `Zone.spec.provider` reference `cloudflare.dns.appthrust.io/v1alpha1`.

Cloudflare API zone types are `full`, `partial`, `secondary`, and `internal`. The dns-api Cloudflare provider does not expose all Cloudflare zone types as an initial user-facing option.

Cloudflare zone type handling:

- `full`: supported by the initial Cloudflare provider. It represents Cloudflare as the authoritative DNS provider for the zone and matches the dns-api `Zone` and `RecordSet` ownership model.
- `internal`: reserved for future private or internal DNS support. It is not supported by `v1alpha1` until the core API has an explicit visibility or internal-zone design.
- `partial`: not supported. It represents a CNAME setup where Cloudflare is not the primary authoritative DNS provider for the whole zone. This is outside the initial dns-api `Zone` and `RecordSet` management model.
- `secondary`: not supported. It represents a secondary DNS setup where another DNS provider is the source of truth and Cloudflare receives the zone through transfer. This conflicts with the initial dns-api model where `RecordSet` desired state is written to the provider API.

Cloudflare `v1alpha1` therefore does not expose `zoneType` in `ZoneClass.spec.parameters`. When creating a Cloudflare zone, the controller always sends `type: "full"` to Cloudflare. A future version may expose `internal` after private or internal DNS behavior is designed.

## Cloudflare ZoneClass Parameters

Cloudflare identity selection uses `ZoneClass.spec.identityRef.name`. The selected identity resource is a `CloudflareIdentity` in the same namespace as the `ZoneClass`.

A typical Cloudflare `ZoneClass`:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: ZoneClass
metadata:
  namespace: tenant-a-platform
  name: cloudflare-public
spec:
  provider:
    name: cloudflare.dns.appthrust.io
    version: v1alpha1
  identityRef:
    name: cloudflare-ci
  parameters:
    zoneCreationPolicy: Create
    zoneDeletionPolicy: Retain
  allowedZones:
    namespaces:
      from: Selector
      selector:
        matchLabels:
          appthrust.io/tenant: tenant-a
```

Cloudflare `ZoneClass.spec.parameters` fields initially contain:

- `zoneCreationPolicy`: whether new Cloudflare zone creation is allowed. Values: `Create` or `Deny`. Default: `Create`.
- `zoneDeletionPolicy`: whether the Cloudflare zone is deleted when the core `Zone` is deleted. Values: `Delete` or `Retain`. Default: `Retain`.

Cloudflare `Provider` schema descriptions for `zoneClass.schemas.parameters`:

| Field | `description` |
| --- | --- |
| `zoneCreationPolicy` | `Controls whether dns-api may create a Cloudflare zone when a Zone does not adopt an existing zone.` |
| `zoneDeletionPolicy` | `Controls whether dns-api deletes the Cloudflare zone when the Kubernetes Zone is deleted.` |

Cloudflare `ZoneClass.spec.parameters` intentionally does not contain:

- `identityRef`: identity selection is the core `ZoneClass.spec.identityRef`.
- `accountID`: account identity is observed from `CloudflareIdentity.status.account.id`, not duplicated into desired state.
- `zoneType`: initial Cloudflare zones are always created as `full`; `internal` needs a later private or internal DNS design.
- `sameNameZonePolicy`: Cloudflare same-name zones are not modeled as an allowed creation mode.
- `jump_start`: Cloudflare DNS import or scan is not part of GitOps-managed desired state.
- `paused`: this belongs to broader Cloudflare application or proxy behavior, not DNS zone creation policy for dns-api.
- `vanity_name_servers`: this is plan-dependent advanced behavior and is outside the initial provider scope.

When the Cloudflare controller creates a zone, it sends the minimum DNS API state to Cloudflare:

```json
{
  "account": {
    "id": "<CloudflareIdentity.status.account.id>"
  },
  "name": "<Zone.spec.domainName>",
  "type": "full"
}
```

The controller does not set `jump_start`. The important dns-api behavior is that provider-side DNS import or scan is not requested and external DNS records are not silently imported into desired state. If the Cloudflare client library requires an explicit value, the controller sends `jump_start: false`.

Cloudflare `ZoneClass.status.conditions[Accepted]` behavior:

- `Accepted=Unknown`, reason `IdentityNotResolved`: the referenced `CloudflareIdentity` does not exist, or the referenced `CloudflareIdentity` is not resolved enough to decide.
- `Accepted=False`, reason `InvalidIdentityRef`: `spec.identityRef.name` is empty, or the referenced `CloudflareIdentity` is `Accepted=False`.
- `Accepted=False`, reason `InvalidParameters`: `zoneCreationPolicy` or `zoneDeletionPolicy` is outside the provider schema.
- `Accepted=False`, reason `DeniedByPolicy`: platform policy rejects the `ZoneClass`.
- `Accepted=True`, reason `Accepted`: provider, identity reference, parameters, and `allowedZones` are statically accepted.

`CloudflareIdentity Ready=False` does not make `ZoneClass Accepted=False` when the identity spec is otherwise valid. Live credential readiness is surfaced on `CloudflareIdentity.status` and on referencing `Zone` resources as `Programmed=False`, reason `ProviderIdentityNotReady`. This matches the Route 53 identity readiness model.

Cloudflare `ZoneClass.spec.parameters` does not contain `sameNameZonePolicy`. Route 53 can create multiple same-name public hosted zones in the same account, so Route 53 needs `sameNameZonePolicy`. Cloudflare zones are modeled as account-scoped domains and the initial Cloudflare provider treats an existing same-name zone as a conflict unless the core `Zone` explicitly adopts it through `Zone.spec.adoption`.

Cloudflare `Zone.spec.adoption` shape:

```yaml
spec:
  adoption:
    zoneID: 023e105f4ecef8ad9ca31a8372d0c353
```

Cloudflare provider IDs are represented in dns-api as 32-character lowercase hexadecimal strings. This applies to Cloudflare zone IDs and DNS record IDs in adoption input and in public status provider data. Uppercase hex, non-hex characters, shorter IDs, and longer IDs are invalid and are not normalized.

`zoneID` is a Cloudflare zone ID. It is a 32-character lowercase hexadecimal Cloudflare identifier. Adoption by zone name alone is not supported because the account may contain stale, moved, or otherwise unsuitable zones and because `zoneID` is the stable provider identifier used by Cloudflare DNS record APIs.

Cloudflare `Provider` schema descriptions for `zone.schemas.adoption`:

| Field | `description` |
| --- | --- |
| `zoneID` | `Cloudflare zone ID of the existing full zone to adopt. Must be a 32-character lowercase hexadecimal string.` |

When adoption is specified, the Cloudflare controller calls Cloudflare `GET /zones/{zone_id}` using `ZoneUnit.spec.zone.adoption.zoneID`. It verifies that the zone account matches `CloudflareIdentity.status.account.id`, verifies that the zone name matches `ZoneUnit.spec.zone.domainName`, and verifies that `type` is `full`. If the zone does not exist, set `Programmed=False`, reason `ExternalResourceNotFound`. If the account, name, or type differs, set `Programmed=False`, reason `ExternalResourceMismatch`. For `v1alpha1`, adopted zones with type `partial`, `secondary`, or `internal` are mismatches.

When adoption succeeds, the Cloudflare controller writes Cloudflare-assigned name servers to `Zone.status.nameServers`, writes `Zone.status.provider.data.zone.id`, `zone.status`, and `zone.type`, and sets `Programmed=True` when the observed state otherwise satisfies the Cloudflare Zone reconciliation rules.

When `Zone.spec.adoption` is absent, the Cloudflare controller lists zones with the observed account ID and `Zone.spec.domainName` before creating a zone. If no same-name zone exists and `zoneCreationPolicy=Create`, it creates a Cloudflare zone with `type: "full"`. If a same-name zone already exists, it sets `Programmed=False`, reason `ProviderConflict`, and requires explicit adoption. If Cloudflare returns more than one same-name zone, the controller also sets `ProviderConflict`; multiple same-name zones are not a supported Cloudflare provider shape.

The Cloudflare controller never automatically adopts a same-name zone. A same-name zone found by list is only a conflict signal unless `ZoneUnit.spec.zone.adoption.zoneID` explicitly names that zone.

Adopted zones follow `ZoneClass.spec.parameters.zoneDeletionPolicy` on core `Zone` deletion. With `Retain`, the Cloudflare zone remains and the controller removes no provider-side zone. With `Delete`, the controller deletes the adopted Cloudflare zone before removing the finalizer. The default policy remains `Retain`, which is the safer default for adopted zones.

## Cloudflare Zone Provider Data

Cloudflare-specific public zone data is stored in `Zone.status.provider.data`, validated by Cloudflare Provider version `zone.schemas.statusProviderData`. `zone.id` is public because users can copy it into adoption settings, use it to open the Cloudflare dashboard, and include it in support or troubleshooting workflows. `zone.status` and `zone.type` are public because they explain common user-visible Cloudflare states such as `pending` delegation and `full` zone requirements.

Core `Zone` status owns common values. Cloudflare `Zone.status.provider.data` must not duplicate `Zone.spec.domainName`, `Zone.status.nameServers`, `Zone.status.conditions`, or Cloudflare account data already observed on `CloudflareIdentity.status.account`.

When the Cloudflare controller has created, adopted, or confirmed a zone, it writes Cloudflare-assigned name servers to core `Zone.status.nameServers`.

Cloudflare `Zone.status.provider.data` fields:

- `zone`: observed Cloudflare zone metadata required for reconciliation.
- `zone.id`: Cloudflare zone ID. Required after the Cloudflare zone is created, adopted, or confirmed. It is a 32-character lowercase hexadecimal string. Child `RecordSet` reconciliation uses this ID for Cloudflare DNS record APIs.
- `zone.status`: Cloudflare zone status, such as `initializing`, `pending`, `active`, or `moved`. Required when returned by Cloudflare.
- `zone.type`: Cloudflare zone type returned by Cloudflare. For `v1alpha1`, the expected value is `full`.

Cloudflare `Provider` schema descriptions for `zone.schemas.statusProviderData`:

| Field | `description` |
| --- | --- |
| `zone` | `Observed Cloudflare zone metadata exposed for adoption and troubleshooting workflows.` |
| `zone.id` | `Cloudflare zone ID used for zone and DNS record API calls. Must be a 32-character lowercase hexadecimal string.` |
| `zone.status` | `Cloudflare zone status returned by the Cloudflare API.` |
| `zone.type` | `Cloudflare zone type returned by the Cloudflare API. The v1alpha1 provider expects full zones.` |

The Cloudflare controller validates public status provider data with the same ID format rules used for adoption input before writing `ZoneUnit.status`. If Cloudflare returns a missing, uppercase, non-hex, short, or long zone ID in a response where a zone ID is required, the controller treats the response as a provider response mismatch or error. It does not write invalid `Zone.status.provider.data`; it reports `Programmed=False` with a provider error or mismatch reason that matches the operation.

`Zone.status.provider.data` intentionally does not contain:

- `zone.name`: duplicate of `Zone.spec.domainName`.
- `accountID` or `accountName`: duplicate of `CloudflareIdentity.status.account`.
- `nameServers`: duplicate of core `Zone.status.nameServers`.
- `originalNameServers`: Cloudflare migration diagnostic data that is not required for reconciliation.
- `createdOn`, `modifiedOn`, or `activatedOn`: optional Cloudflare diagnostics that are not required for the initial controller or RecordSet reconciliation.

Example:

```yaml
status:
  nameServers:
    - everton.ns.cloudflare.com
    - kira.ns.cloudflare.com
  provider:
    data:
      zone:
        id: 023e105f4ecef8ad9ca31a8372d0c353
        status: pending
        type: full
```

## Cloudflare Zone Reconciliation

Cloudflare Zone reconciliation follows the same condition model as Route 53. `Accepted=True` means Kubernetes references, namespace policy, provider identity reference, and provider policy are acceptable before Cloudflare zone mutation. `Programmed=True` means the last Cloudflare API state observed by the controller matches the dns-api desired state. It does not mean public DNS can resolve the zone.

Cloudflare `Zone Accepted=True` requires:

- referenced `ZoneClass` exists.
- `ZoneClass.status.conditions[Accepted]=True`.
- `Zone` matches `ZoneClass.spec.allowedZones`.
- `Zone.spec.provider` matches `ZoneClass.spec.provider`.
- `Provider/cloudflare.dns.appthrust.io` version `v1alpha1` resolves and is handled by `cloudflare.dns.appthrust.io/controller`.
- `ZoneClass.spec.identityRef.name` resolves to a `CloudflareIdentity` in the `ZoneClass` namespace.
- `CloudflareIdentity.status.conditions[Accepted]=True`.
- Cloudflare `ZoneClass.spec.parameters` pass provider policy.

If the referenced `CloudflareIdentity` is `Accepted=True` but not `Ready=True`, the Cloudflare controller sets `Zone Accepted=True` and `Zone Programmed=False`, reason `ProviderIdentityNotReady`. Accepted `RecordSet` targets for that zone also report `Programmed=False`, reason `ProviderIdentityNotReady`. Detailed credential information remains on `CloudflareIdentity.status`.

Cloudflare `Zone Programmed=True` requires:

- `CloudflareIdentity.status.conditions[Ready]=True`.
- the Cloudflare zone exists.
- the observed Cloudflare zone account matches `CloudflareIdentity.status.account.id`.
- the observed Cloudflare zone name matches `Zone.spec.domainName`.
- the observed Cloudflare zone type is `full`.
- core `Zone.status.nameServers` contains the Cloudflare-assigned name servers returned by Cloudflare.
- `Zone.status.provider.data.zone.id`, `zone.status`, and `zone.type` reflect the last observed Cloudflare zone.
- no provider-side change submitted by the controller is waiting for completion.

Cloudflare `zone.status=pending` is compatible with `Zone Programmed=True` when the other observed state matches. In Cloudflare, `pending` means the zone exists in Cloudflare but parent-zone NS delegation has not been completed. Parent-zone delegation, public resolver propagation, and resolver reachability are not part of `Programmed=True`, matching the core Zone condition contract.

Cloudflare `zone.status=active` is compatible with `Zone Programmed=True` when the other observed state matches.

Cloudflare `zone.status=initializing` is treated as `Programmed=False`, reason `ProviderChangePending`, unless Cloudflare later documents that it is a stable user-action state. The controller re-observes the zone on a short retry interval.

Cloudflare `zone.status=moved` is treated as `Programmed=False`, reason `ExternalResourceMismatch` for explicitly adopted zones and `ProviderConflict` for created zones. The controller does not try to repair a moved zone by creating a replacement while the same core `Zone` still points at the old Cloudflare zone ID.

When `Zone.spec.adoption` is absent and `zoneCreationPolicy=Deny`, the Cloudflare controller sets `Zone Accepted=False`, reason `DeniedByPolicy`. `zoneCreationPolicy=Deny` is for adoption-only classes.

When `Zone.spec.adoption` is absent and a same-name zone already exists in the Cloudflare account, the Cloudflare controller sets `Zone Programmed=False`, reason `ProviderConflict`, and requires explicit adoption.

When `ZoneUnit.spec.zone.adoption.zoneID` is set and the zone does not exist, the Cloudflare controller sets `Zone Programmed=False`, reason `ExternalResourceNotFound`. If the zone exists but account, name, or type differs, it sets `Zone Programmed=False`, reason `ExternalResourceMismatch`.

The Cloudflare controller re-observes zones even in stable state. If Cloudflare status or assigned name servers change, it updates `Zone.status` without changing `Zone.spec`. If observed account, name, or type differs from the managed identity and desired state, it treats the difference as mismatch or conflict instead of silently adopting another zone.

Cloudflare controller also enqueues a `ZoneUnit` when the Core ZoneUnit Controller copies `Zone.metadata.annotations["dns.appthrust.io/reconcile-request"]` to the matching `ZoneUnit`. This requests re-observation of the Cloudflare zone without waiting for periodic re-observation. Updating the annotation alone must not set `Programmed=False` or send no-op Cloudflare API changes when external state already matches `ZoneUnit.spec`.

## Cloudflare Zone Deletion

Cloudflare `zoneDeletionPolicy` follows the same high-level contract as Route 53, but Cloudflare API behavior differs. A local development account check on 2026-06-04 confirmed that Cloudflare accepts `DELETE /zones/{zone_id}` while a user-created DNS record still exists in the zone. The delete succeeds, the zone disappears from `GET /zones`, and the record namespace disappears with the zone.

Because Cloudflare does not protect zone deletion by rejecting zones that still contain DNS records, the Core ZoneUnit Controller must keep accepted `RecordSet` items in `ZoneUnit.spec.recordSets[]` until record cleanup is complete. The Cloudflare ZoneUnit controller must not delete the Cloudflare zone while accepted record-set items remain.

When `zoneDeletionPolicy=Retain`:

- the Cloudflare controller removes its provider `ZoneUnit` finalizer after recording cleanup completion.
- deleting the core `Zone` does not delete the Cloudflare zone.
- if policy changes from `Delete` to `Retain` while `ZoneUnit` is not being deleted, the controller does not start provider deletion.
- existing Cloudflare records remain provider-side.

When `zoneDeletionPolicy=Delete`:

- the Cloudflare controller keeps a provider finalizer on `ZoneUnit`.
- when `ZoneUnit.metadata.deletionTimestamp` is set, the controller first checks `ZoneUnit.spec.recordSets[]`.
- if any record-set item remains, the controller does not call Cloudflare `DELETE /zones/{zone_id}`. It keeps the provider finalizer and sets `Programmed=False`, reason `ProviderConflict`.
- after record-set items are gone, the controller deletes the Cloudflare zone and removes the provider finalizer after Cloudflare confirms deletion.

Cloudflare zone deletion target resolution order:

1. `ZoneUnit.spec.zone.adoption.zoneID`.
2. `ZoneUnit.status.zone.provider.data.zone.id`.

The Cloudflare controller never derives a delete target from a same-name zone list. If no delete target can be resolved, it removes the provider finalizer because there is no known provider-side zone owned by this `ZoneUnit`. If Cloudflare reports the target zone does not exist, deletion is considered complete and the provider finalizer is removed.

Cloudflare zone deletion error mapping:

- permission failure: `Programmed=False`, reason `ProviderAccessDenied`.
- temporary Cloudflare API failure, rate limit, or 5xx: `Programmed=False`, reason `ProviderUnavailable`.
- invalid delete request or Cloudflare conflict: `Programmed=False`, reason `ProviderInvalidRequest` or `ProviderConflict`, depending on the Cloudflare error.

Detailed Cloudflare ZoneUnit pseudocode is not yet split into the ZoneUnit pseudocode directory. Until it is written, this document defines the Cloudflare provider behavior.

Example Cloudflare Provider:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: Provider
metadata:
  name: cloudflare.dns.appthrust.io
spec:
  display:
    name: Cloudflare DNS
    description: Public DNS zones and record sets managed through Cloudflare DNS.
    logo:
      url: https://cdn.simpleicons.org/cloudflare
  versions:
    - name: v1alpha1
      served: true
      storage: true
      deprecated: false
      identity:
        resource:
          group: cloudflare.dns.appthrust.io
          kind: CloudflareIdentity
          scope: Namespaced
      zoneClass:
        schemas:
          parameters:
            openAPIV3Schema:
              type: object
              properties:
                zoneCreationPolicy:
                  type: string
                  enum:
                    - Create
                    - Deny
                  default: Create
                zoneDeletionPolicy:
                  type: string
                  enum:
                    - Delete
                    - Retain
                  default: Retain
      zone:
        schemas:
          adoption:
            openAPIV3Schema:
              type: object
              required:
                - zoneID
              properties:
                zoneID:
                  type: string
                  minLength: 32
                  maxLength: 32
                  pattern: '^[0-9a-f]{32}$'
          statusProviderData:
            openAPIV3Schema:
              type: object
              properties:
                zone:
                  type: object
                  required:
                    - id
                  properties:
                    id:
                      type: string
                      minLength: 32
                      maxLength: 32
                      pattern: '^[0-9a-f]{32}$'
                    status:
                      type: string
                    type:
                      type: string
      recordSet:
        supportedTypes:
          - A
          - AAAA
          - TXT
          - CNAME
          - MX
          - CAA
          - NS
        schemas:
          options:
            openAPIV3Schema:
              type: object
              additionalProperties: false
              properties:
                ttl:
                  type: string
                  enum:
                    - Auto
                proxied:
                  type: boolean
                  default: false
                comment:
                  type: string
                  maxLength: 100
                tags:
                  type: array
                  maxItems: 20
                  items:
                    type: string
          adoption:
            openAPIV3Schema:
              type: object
              required:
                - recordIDs
              properties:
                recordIDs:
                  type: array
                  minItems: 1
                  uniqueItems: true
                  items:
                    type: string
                    minLength: 32
                    maxLength: 32
                    pattern: '^[0-9a-f]{32}$'
          statusProviderData:
            openAPIV3Schema:
              type: object
              properties:
                records:
                  type: array
                  uniqueItems: true
                  items:
                    type: object
                    required:
                      - id
                    properties:
                      id:
                        type: string
                        minLength: 32
                        maxLength: 32
                        pattern: '^[0-9a-f]{32}$'
        disableValidations:
          - name: require-ttl
            when: "has(self.spec.options) && has(self.spec.options.ttl) && self.spec.options.ttl == 'Auto'"
          - name: forbid-cname-apex
            when: "self.spec.type == 'CNAME' && self.spec.name == '@'"
        validationRules:
          - rule: "self.spec.type == 'A' || self.spec.type == 'AAAA' || self.spec.type == 'TXT' || self.spec.type == 'CNAME' || self.spec.type == 'MX' || self.spec.type == 'CAA' || self.spec.type == 'NS'"
            message: "cloudflare supports A, AAAA, TXT, CNAME, MX, CAA, and delegated NS records"
          - rule: "!(has(self.spec.options) && has(self.spec.options.ttl) && has(self.spec.ttl))"
            message: "ttl must not be specified when cloudflare automatic ttl is used"
          - rule: "(has(self.spec.options) && has(self.spec.options.ttl) && self.spec.options.ttl == 'Auto') || (has(self.spec.ttl) && self.spec.ttl >= 60 && self.spec.ttl <= 86400)"
            message: "cloudflare fixed ttl must be between 60 and 86400 seconds"
          - rule: "!(has(self.spec.options) && has(self.spec.options.proxied) && self.spec.options.proxied) || self.spec.type == 'A' || self.spec.type == 'AAAA' || self.spec.type == 'CNAME'"
            message: "cloudflare proxied is supported only for A, AAAA, and CNAME records"
          - rule: "!(has(self.spec.options) && has(self.spec.options.proxied) && self.spec.options.proxied) || (has(self.spec.options.ttl) && self.spec.options.ttl == 'Auto' && !has(self.spec.ttl))"
            message: "cloudflare proxied records must use automatic ttl"
          - rule: "!(self.spec.type == 'MX' && self.spec.mx.records.exists(r, r.exchange == '.'))"
            message: "cloudflare provider does not support Null MX until Cloudflare API support is verified"
      zoneUnit:
        disableValidations:
          - name: forbid-cname-coexistence
            when: "(self.type == 'CNAME' && other.type in ['TXT', 'MX', 'CAA']) || (other.type == 'CNAME' && self.type in ['TXT', 'MX', 'CAA'])"
        validationRules:
          - rule: "self.name != other.name || !(self.type == 'NS' || other.type == 'NS')"
            message: "cloudflare does not allow NS records to coexist with another record type at the same owner name"
```

## Cloudflare RecordSet Scope

Cloudflare `v1alpha1` supports standard A, AAAA, TXT, CNAME, MX, CAA, and delegated NS `RecordSet` resources. SRV, SVCB, HTTPS, DS, DNSKEY, PTR, SSHFP, URI, and other Cloudflare-supported record types are not exposed until the core API defines standard body fields for them.

Cloudflare allows apex CNAME records through CNAME flattening. The Cloudflare `v1alpha1` Provider disables core validation `forbid-cname-apex` for `type=CNAME` and `spec.name="@"`, so users can declare an apex CNAME with `spec.cname.target`. The controller still treats it as a CNAME desired state in dns-api and lets Cloudflare return flattened DNS responses provider-side. This does not disable CNAME target validation.

Cloudflare intentionally supports some owner-name combinations that portable DNS defaults reject. The Cloudflare `v1alpha1` Provider disables ZoneUnit composition validation `forbid-cname-coexistence` only for `CNAME` with `TXT`, `MX`, and `CAA`. It does not disable conflicts for `CNAME` with `A` or `AAAA`. Cloudflare also declares a ZoneUnit validation rule that rejects `NS` coexisting with any other record type at the same owner name.

Cloudflare does not have a Route 53 ALIAS equivalent in `RecordSet.spec.options`. Zone-wide `settings.flatten_cname`, `settings.ipv4_only`, `settings.ipv6_only`, `private_routing`, Cloudflare tunnel helpers, and automatic origin discovery are outside `v1alpha1`. A future Provider version may expose them as provider options after their GitOps shape and conflict behavior are designed.

Cloudflare `RecordSet.spec.options` shape:

```yaml
spec:
  options:
    ttl: Auto
    proxied: true
    comment: app endpoint
    tags:
      - app:frontend
      - owner:platform
```

Cloudflare `RecordSet.spec.options` fields:

- `ttl`: optional Cloudflare TTL mode. The only initial value is `Auto`, which maps to Cloudflare API `ttl: 1`.
- `proxied`: optional boolean. Default is `false`. It is accepted only for A, AAAA, and CNAME.
- `comment`: optional record comment. It is copied to every Cloudflare DNS record managed for the `RecordSet`. Initial max length is 100 characters so the same manifest is accepted on all Cloudflare plans that support comments.
- `tags`: optional record tags. Each item is a Cloudflare `name:value` tag string and is copied to every Cloudflare DNS record managed for the `RecordSet`.

Cloudflare `Provider` schema descriptions for `recordSet.schemas.options`:

| Field | `description` |
| --- | --- |
| `ttl` | `Cloudflare TTL mode. Auto uses Cloudflare automatic TTL and omits the core spec.ttl value.` |
| `proxied` | `Whether Cloudflare proxying is enabled for this DNS record. Supported only for A, AAAA, and CNAME records.` |
| `comment` | `Optional Cloudflare DNS record comment copied to each provider record managed by this RecordSet.` |
| `tags` | `Optional Cloudflare DNS record tags in name:value form copied to each provider record managed by this RecordSet.` |
| `tags[]` | `One Cloudflare DNS record tag in name:value form.` |

Cloudflare automatic TTL and core TTL are mutually exclusive. If `spec.options.ttl=Auto`, `spec.ttl` is omitted and the controller sends Cloudflare API `ttl: 1`. If `spec.options.ttl` is absent, `spec.ttl` is required by core validation and the controller sends that numeric TTL to Cloudflare. Cloudflare `v1alpha1` accepts fixed TTL values only in the `60..86400` range. This range is enforced by the Cloudflare Provider resource `recordSet.validationRules`, not by a Cloudflare-specific core API field. `spec.ttl: 1` is rejected for Cloudflare even though Cloudflare API `ttl=1` is valid for automatic TTL. Users select automatic TTL only with `spec.options.ttl: Auto`.

Cloudflare Enterprise plans may allow fixed TTL values below 60 seconds, but Cloudflare `v1alpha1` does not support the Enterprise `30..59` range. A future Cloudflare Provider version may expose lower fixed TTL values through a designed provider setting, such as a `ZoneClass.spec.parameters` value, after account or plan capability handling is specified.

The document writer must update `docs/manual` Cloudflare RecordSet guidance and examples when this design is committed. User-facing documentation must show `spec.options.ttl: Auto` as the only automatic TTL form, must not use `spec.ttl: 1` for Cloudflare, and must state the fixed TTL range `60..86400`.

When `spec.options.proxied=true`, `spec.options.ttl=Auto` is required because Cloudflare proxied records use automatic TTL.

`spec.options.proxied` is not accepted for TXT, MX, CAA, or delegated NS. Cloudflare API exposes `proxied` fields broadly in generated schemas, but the dns-api Cloudflare provider only exposes it where the Cloudflare DNS product UI and behavior support proxying hostname traffic: A, AAAA, and CNAME.

Cloudflare record comments and tags are desired state, not ownership metadata. They do not affect DNS responses. The controller does not add hidden ownership tags because Cloudflare record tags are plan-dependent and not available on all accounts. Cleanup and ownership are based on Kubernetes `RecordSet` state, explicit adoption, and `RecordSet.status.provider.data.records[].id`, not on Cloudflare tags.

Cloudflare tag validation:

- each tag is `name:value`.
- `name` is `1..32` characters and contains only ASCII letters, digits, `_`, or `-`.
- `name` must not start with `cf-`, case-insensitive, because Cloudflare reserves `cf-` tags for Cloudflare-specific behavior in import/export workflows.
- `value` is `0..100` characters and must not contain newline or carriage return.
- at most 20 tags are accepted.
- exact duplicate `name:value` pairs are rejected after lowercasing the tag name.

Cloudflare `v1alpha1` rejects Null MX (`exchange: "."`) until live Cloudflare DNS Records API support is verified and designed. Standard MX records remain supported.

## Cloudflare RecordSet API Mapping

Cloudflare CNAME updates patch the existing provider record for the same owner name. The controller does not pair CNAME records by normalized target content because changing `spec.cname.target` must update the same Cloudflare DNS record instead of creating a second same-name CNAME or deleting before creating.

Cloudflare DNS Records API manages individual DNS records by Cloudflare record ID. A dns-api `RecordSet` can contain multiple DNS values, so one Kubernetes `RecordSet` may own multiple Cloudflare DNS records.

The controller computes the full DNS owner name from `RecordSet.spec.name` and `Zone.spec.domainName` and sends Cloudflare API `name` as the full Punycode owner name. It uses `Zone.status.provider.data.zone.id` as the Cloudflare `zone_id`. The controller must not duplicate Cloudflare `zone_id` or account identity in `RecordSet.spec.options` or `RecordSet.status.provider.data`; the zone ID is observed on the parent `Zone`, and the account is observed on `CloudflareIdentity`.

Desired Cloudflare records are derived as follows:

- A: one Cloudflare A record per `spec.a.addresses[]`, with `content` set to the IPv4 address.
- AAAA: one Cloudflare AAAA record per `spec.aaaa.addresses[]`, with `content` set to the IPv6 address.
- CNAME: one Cloudflare CNAME record, with `content` set to `spec.cname.target`.
- TXT: one Cloudflare TXT record per `spec.txt.values[]`, with `content` set to the logical TXT value. The controller lets Cloudflare handle provider-side quoting and chunking and compares observed TXT values as logical TXT strings.
- MX: one Cloudflare MX record per `spec.mx.records[]`, with `content` set to `exchange` and `priority` set to `preference`.
- CAA: one Cloudflare CAA record per `spec.caa.records[]`, with `data.flags`, `data.tag`, and `data.value` set from the dns-api CAA tuple. The controller compares CAA records as unordered `(flags, tag, value)` tuples.
- delegated NS: one Cloudflare NS record per `spec.ns.nameServers[]`, with `content` set to the delegated name server.

For every desired Cloudflare record, the controller sends the same TTL, `proxied`, `comment`, and `tags` values derived from the parent `RecordSet`. For record types where `proxied` is unsupported by dns-api, the controller omits `proxied` from the API request. For A, AAAA, and CNAME, it sends `proxied: false` when `spec.options.proxied` is omitted so DNS-only is explicit desired state.

The controller lists Cloudflare DNS records for the exact full owner name before creating, adopting, updating, or deleting provider-side records. It treats the set of Cloudflare records for the target type and name as the provider-side representation of one dns-api `RecordSet`.

Cloudflare API conflicts are mapped to the common condition model. If Cloudflare refuses A or AAAA because a CNAME already exists at the same owner name, or refuses CNAME because A, AAAA, or CNAME already exists at that owner name, set `Programmed=False`, reason `ProviderConflict`. If Cloudflare refuses delegated NS because another record type exists at the same owner name, or another record type because delegated NS exists at the same owner name, also set `ProviderConflict`. The controller may detect these conflicts by listing before create, but Cloudflare API rejection remains authoritative.

Cloudflare invalid request messages are surfaced as sanitized summaries on the `Programmed=False` condition and the matching Warning Event. For `ProviderInvalidRequest`, the message starts with `Cloudflare API rejected the request` and may include the HTTP status, the first Cloudflare error `code`, the first Cloudflare error `message`, and a short count of additional errors. The controller builds this text from parsed Cloudflare error fields only and bounds the message length. It must not include the access token, identity secret reference, request headers, raw request body, full Kubernetes manifest, raw Cloudflare response body, or SDK stack text. For example, when Cloudflare rejects record tags on an account whose tag quota is zero, the user-visible message is equivalent to `Cloudflare API rejected the request (HTTP 400, code 9300): DNS record has 1 tags, exceeding the quota of 0.`.

When `RecordSet.spec.adoption` is absent:

- if no Cloudflare records for the target type and name exist and no Cloudflare same-name conflict exists, the controller creates the desired Cloudflare records.
- if Cloudflare records for the target type and name exist and `RecordSet.status.provider.data.records[].id` identifies records previously managed by this `RecordSet`, the controller updates, creates, or deletes Cloudflare records to make the provider-side set match `RecordSet.spec`.
- if Cloudflare records for the target type and name exist but the `RecordSet` has neither matching managed status nor explicit adoption, the controller does not auto-adopt them. It sets `Programmed=False`, reason `ProviderConflict`.

Cloudflare `RecordSet.spec.adoption` shape:

```yaml
spec:
  adoption:
    recordIDs:
      - 023e105f4ecef8ad9ca31a8372d0c353
```

`recordIDs` is the complete unordered set of Cloudflare DNS record IDs that represent the dns-api `RecordSet`. Every item is a 32-character lowercase hexadecimal string. Duplicate IDs are rejected because the array is a complete unordered set, not an ordered list of operations. For CNAME it contains exactly one ID. For multi-value A, AAAA, TXT, MX, CAA, and delegated NS, it contains one ID per desired value.

Cloudflare `Provider` schema descriptions for `recordSet.schemas.adoption`:

| Field | `description` |
| --- | --- |
| `recordIDs` | `Cloudflare DNS record IDs to adopt for this RecordSet. Items are unique because the array is a complete unordered set.` |
| `recordIDs[]` | `One Cloudflare DNS record ID. Must be a 32-character lowercase hexadecimal string.` |

When adoption is specified, the Cloudflare controller fetches or lists the referenced Cloudflare records and verifies that every record belongs to the parent `Zone.status.provider.data.zone.id`, has the expected owner name and type, and that the unordered set of content, priority or CAA data, TTL, proxied state, comment, and tags matches `RecordSet.spec`. If any ID does not exist, set `Programmed=False`, reason `ExternalResourceNotFound`. If the IDs exist but do not exactly represent the desired `RecordSet`, set `Programmed=False`, reason `ExternalResourceMismatch`. The controller never adopts additional same-name records not named by `recordIDs`. After adoption has written `RecordSet.status.provider.data.records[].id`, the unordered status ID set must continue to match `RecordSet.spec.adoption.recordIDs` while adoption remains specified. If the sets differ, set `Accepted=False`, reason `ManagedResourceMismatch`, and do not update or delete Cloudflare DNS records using the stale status IDs.

Cloudflare-specific public record-set information is stored in `RecordSet.status.provider.data`, validated by Cloudflare Provider version `recordSet.schemas.statusProviderData`. It contains Cloudflare DNS record IDs because users can copy them into adoption settings, inspect the records in the Cloudflare dashboard, and use them in support workflows. Observed provider record details such as owner name, content, TTL, proxy state, comments, and tags are controller-internal reconciliation state and are stored in `RecordSet.status.provider.state`.

Cloudflare `RecordSet.status.provider.data` fields:

- `records`: Cloudflare DNS record IDs currently managed by the Kubernetes `RecordSet`. IDs are unique. The array is unordered.
- `records[].id`: Cloudflare DNS record ID. Required after create or adoption. It is a 32-character lowercase hexadecimal string.

Cloudflare `Provider` schema descriptions for `recordSet.schemas.statusProviderData`:

| Field | `description` |
| --- | --- |
| `records` | `Cloudflare DNS record IDs currently managed for this RecordSet. IDs are unique and unordered.` |
| `records[]` | `One Cloudflare DNS record managed for this RecordSet.` |
| `records[].id` | `Cloudflare DNS record ID used for adoption, update, and delete operations. Must be a 32-character lowercase hexadecimal string.` |

The Cloudflare controller validates `RecordSet.status.provider.data.records[].id` with the same format rules used for `RecordSet.spec.adoption.recordIDs[]` before writing `ZoneUnit.status`. If Cloudflare returns duplicate record IDs or an ID that is not a 32-character lowercase hexadecimal string, the controller treats the response as a provider response mismatch or error. It does not write invalid or duplicate public status provider data; it reports `Programmed=False` with a provider error or mismatch reason that matches the operation.

`RecordSet.status.provider.data` must not contain Cloudflare account ID, account name, parent zone ID, parent zone status, observed record name, type, content, priority, TTL, proxy state, comment, tags, or data already represented in core `RecordSet.spec`.

Before creating, adopting, or updating Cloudflare DNS records, the controller ensures the provider finalizer exists on the owning `ZoneUnit`. During deletion, it deletes Cloudflare records by `ZoneUnit.status.recordSets[].provider.data.records[].id`. If status is empty but `ZoneUnit.spec.recordSets[].adoption.recordIDs` exists, the controller may use those IDs after verifying they still match the deleting record-set item. It must not delete arbitrary same-name Cloudflare records discovered only by list when no status or adoption ID proves ownership.

The Cloudflare controller uses the Cloudflare batch DNS record API when applying multiple create, patch, or delete operations for one dns-api `RecordSet`. A dns-api `RecordSet` with multiple values is programmed as one logical unit: the controller must not create, patch, or delete only the first Cloudflare DNS record and wait for a later reconcile to apply the remaining values. If a batch request fails, only the `RecordSet` instances whose operations were included in that request are reported as `Programmed=False`; unrelated `RecordSet` instances in the same `ZoneUnit` must not be marked failed by that batch error.

After a successful batch request, the affected `RecordSet` is reported as `Programmed=False`, reason `ProviderChangePending`, until a later observation confirms that every desired Cloudflare DNS record for that `RecordSet` is present, every obsolete managed Cloudflare DNS record is absent, and provider-specific fields such as TTL, proxy state, comments, tags, priority, and CAA data match the desired state. `Programmed=True` is set only after the complete logical `RecordSet` matches the observed Cloudflare API state. A partially observed multi-value `RecordSet` is `Programmed=False`, not `Programmed=Unknown`, because the controller can determine that the provider state is not yet converged.

Cloudflare executes the batch request in one provider-side transaction, but Cloudflare edge propagation is not atomic. DNS resolvers may temporarily observe only part of a successful batch while Cloudflare propagates individual records. dns-api status is based on Cloudflare API observation, not resolver-side propagation.

Cloudflare-specific RecordSet Events do not replace common Events. Reasons start with `Cloudflare`.

Cloudflare-specific RecordSet Events:

| type | reason | related | When emitted |
| --- | --- | --- | --- |
| `Normal` | `CloudflareRecordCreated` | `Zone` | a Cloudflare DNS record is created for a `RecordSet` value. |
| `Normal` | `CloudflareRecordUpdated` | `Zone` | a Cloudflare DNS record is updated to match `RecordSet.spec`. |
| `Normal` | `CloudflareRecordDeleted` | `Zone` | a Cloudflare DNS record is deleted during reconciliation or `RecordSet` deletion. |
| `Normal` | `ExternalResourceAdopted` | `Zone` | all explicitly referenced Cloudflare DNS records are brought under management. |

Detailed Cloudflare record-set ZoneUnit pseudocode is not yet split into the ZoneUnit pseudocode directory. Until it is written, this document defines the Cloudflare provider behavior.
