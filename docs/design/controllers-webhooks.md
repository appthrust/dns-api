# Controllers and Webhooks

## Controller Contract

The dns-api core provides common APIs, CRD schema, validation, conditions, conformance-test contracts, and the Core ZoneUnit Controller. The core does not operate DNS provider APIs. Provider controllers reconcile `ZoneUnit` resources and provider-specific resources according to this contract.

Provider-independent safety and ownership arbitration are provided by the Core ZoneUnit Controller. It builds `ZoneUnit.spec`, keeps record ownership stable, rejects conflicting claims, and projects provider results back to claim status.

Provider controllers determine scope by both `ZoneUnit.spec.provider` and `ZoneClass.spec.controllerName`. A provider controller first resolves `ZoneUnit.spec.zone.zoneClassRef`. If `ZoneClass.spec.controllerName` does not match its own controller name, it leaves the `ZoneUnit` alone. If the controller name matches, the controller checks that the resource's `spec.provider.name` and `version` exactly match its runtime provider binding. If the Provider reference is not configured for that controller, the `ZoneClass` cannot be resolved, or the Provider contract is unsupported by that controller, it reports `Accepted=False` and does not operate provider APIs for that `ZoneUnit`.

Provider controllers are not required to use one reconciler per provider API concept. They use `ZoneUnit` as the Kubernetes reconciliation target. A provider such as Route 53 may reconcile a hosted zone and all child record sets together because provider APIs list and batch by hosted zone. A provider such as Cloudflare may still structure its code around separate zone and DNS record API clients.

Status ownership:

- `ZoneClass.status`: updated by the provider controller whose name matches `ZoneClass.spec.controllerName`.
- `ZoneUnit.spec` and the core `ZoneUnit` finalizer: updated by the Core ZoneUnit Controller.
- `ZoneUnit.status` and the provider `ZoneUnit` finalizer: updated by the provider controller whose name matches `ZoneClass.spec.controllerName`.
- `Zone.status`: updated by the Core ZoneUnit Controller by projecting `ZoneUnit.status.zone`.
- `RecordSet.status`: updated by the Core ZoneUnit Controller by projecting `ZoneUnit.status.recordSets[]`.
- Provider-specific identity status: updated by that provider controller. For Route 53, `Route53Identity.status` is updated by the Route 53 controller.

Admission webhooks perform spec validation, status validation, and defaulting only. They do not update status. The core validating webhook references `Provider` for provider-specific inline object schema validation and CEL validation. RBAC grants status subresource update permissions only to provider controllers responsible for the target resources.

Admission validation must not reject `Zone`, `RecordSet`, or `ZoneUnit` solely because the referenced `ZoneClass` is missing or because the resource `spec.provider` differs from the referenced `ZoneClass.spec.provider`. GitOps may apply dependent resources before their referenced `ZoneClass`. Reference resolution and cross-resource Provider mismatch are reported through conditions by controllers instead. `spec.provider` presence, immutability, referenced `Provider` schema validation, and provider-specific inline payload validation remain admission concerns because they are self-contained for the admitted object and its declared Provider.

All claim conditions include `observedGeneration`. If a resource moves out of a provider controller's scope, that controller stops updating `ZoneUnit.status`. Existing claim conditions can be recognized as old through `observedGeneration`.

When projecting a current provider condition from `ZoneUnit.status.zone` or `ZoneUnit.status.recordSets[]`, core preserves `Status`, `Reason`, and `Message`, including an empty Message. This applies to Accepted and Programmed on both Zone and RecordSet, without changing UID/generation fences or acceptance precedence. Core-generated waiting/rejection conditions retain their own messages.

Provider implementation differences are controlled by common conditions, reasons, status ownership, support levels, and conformance tests. The core does not hide differences by providing one shared DNS controller implementation; providers give a consistent user experience by satisfying the same contract.

## Provider Enablement

Provider enablement gates runtime registrations and provider-only privileges, not just Provider manifests. The manager accepts these flags and environment defaults; explicit flags take precedence:

| Chart value | Manager flag | Environment variable | Default |
| --- | --- | --- | --- |
| `providers.route53.enabled` | `--route53-controller-enabled` | `ROUTE53_CONTROLLER_ENABLED` | true |
| `providers.cloudflare.enabled` | `--cloudflare-controller-enabled` | `CLOUDFLARE_CONTROLLER_ENABLED` | true |
| `providers.route53.endpoint.conversionAPI.enabled` | `--route53-endpoint-conversion-enabled` | `ROUTE53_ENDPOINT_CONVERSION_ENABLED` | true |
| `providers.external.enabled` | `--external-controller-enabled` | `EXTERNAL_CONTROLLER_ENABLED` | false |
| `providers.hosted.enabled` | `--hosted-controller-enabled` | `HOSTED_CONTROLLER_ENABLED` | false |

Each provider switch gates its identity, ZoneClass, and ZoneUnit controllers, credential-client construction, and identity spec/status admission handler. The chart gates matching identity RBAC and webhook entries. Secret get/list/watch belongs only to enabled Cloudflare; mounted serving certificates do not require Secret API reads. Route 53 conversion requires both Route 53 flags. Its chart flag, capability, APIService, and conversion-create permission additionally require the webhook Service to be enabled. The external switch gates the external ZoneClass/ZoneUnit controllers, the external Provider and `EndpointProviderCapability`, its conversion handler/APIService, and conversion-create RBAC; external has no identity controller, identity webhook, or credential reads, and its conversion API requires the webhook Service with cert-manager-managed serving TLS ([External Provider](external-provider.md#packaging-and-metrics)).

The hosted switch gates its ZoneClass/ZoneUnit controllers, Provider and `EndpointProviderCapability`, conversion handler/APIService, snapshot-write and conversion-create RBAC, and the separate authoritative server Deployment/Service/ServiceAccount. Like external, hosted has no identity or credential reads and requires the webhook Service with cert-manager-managed serving TLS. The server can only get/list/watch `HostedZone` and patch `HostedZone/status`; the manager has no hosted status-write permission. Empty `providers.hosted.server.watchNamespace` means all namespaces and uses a ClusterRole/ClusterRoleBinding. An explicit namespace uses a Role/RoleBinding in that namespace, bound to the server ServiceAccount in the release namespace. Hosted CRDs remain installed when disabled; no hosted runtime templates render.

Core composition, endpoint translation, namespace reads, and core schema/status admission remain enabled independently. Gateway/Service source switches retain their existing registration and permission gates. Scheme/CRD types remain available when a provider is disabled. Helm's default admission coverage matches Kustomize, including ZoneClass status and both identity spec/status endpoints. Kustomize's default installation enables Route 53 and Cloudflare, with external disabled. The opt-in `app/operator/config/external` overlay adds the external Provider, capability, cert-manager-backed APIService, manager arguments, and only its conversion-create permission; it does not add identity or Secret privileges. Custom disable overlays must remove the corresponding admission entries and privileges as well as setting manager flags. Disabling a provider does not complete existing finalizers.

## Implementation Modules

Implementation modules are controller ownership boundaries. One Kubernetes controller has one owning module, and one owning module owns at most one controller. The owning module contains the reconciler entrypoint, watch mapping, finalizer writes, status writes, and controller-specific planning. Shared helpers used by more than one controller must live in a non-controller module and must not own watches, finalizers, or status writes.

Initial controller modules:

- `internal/core/controllers/zoneunit`: Core ZoneUnit Controller entrypoint, watch mapping, `ZoneUnit.spec` building, claim status projection, and core finalizer flow.
- `internal/providers/route53/controllers/zoneunit`: Route 53 ZoneUnit Controller entrypoint, `ZoneUnit` watch, hosted zone reconciliation, record-set batch planning, provider finalizer flow, and `ZoneUnit.status` construction.
- `internal/providers/route53/controllers/identity`: Route 53 identity controller entrypoint and `Route53Identity.status` ownership.
- `internal/providers/route53/controllers/zoneclass`: Route 53 ZoneClass controller entrypoint and Route 53 `ZoneClass.status` ownership.
- `internal/providers/cloudflare/controllers/zoneunit`: Cloudflare ZoneUnit Controller entrypoint, `ZoneUnit` watch, Cloudflare zone and DNS record reconciliation, provider finalizer flow, and `ZoneUnit.status` construction.
- `internal/providers/cloudflare/controllers/identity`: Cloudflare identity controller entrypoint and `CloudflareIdentity.status` ownership.
- `internal/providers/cloudflare/controllers/zoneclass`: Cloudflare ZoneClass controller entrypoint and Cloudflare `ZoneClass.status` ownership.

Provider controller modules may split implementation files by provider API concept, such as `zone.go` and `recordset.go`, but only the `zoneunit` controller module owns the `ZoneUnit` watch for that provider. Helper types inside a controller module are not separate Kubernetes controllers unless the module exposes and registers a controller entrypoint for them.

Initial non-controller modules:

- `internal/core/providercontract`: Provider version resolution, served/storage version checks, schema validation, and provider payload conversion.
- `internal/core/celruntime`: shared KRO CEL runtime adapter used by Provider validation rules, validation toggles, ZoneUnit composition rules, and provider payload conversion.
- `internal/core/declaration`: declarative composition and provider-policy rule evaluation.
- `internal/core/webhook`: core admission webhook registration, request handlers, status validation, Provider schema validation, Provider conversion webhook client validation, and admission defaulting boundaries.
- `internal/providers/route53/webhook`: Route 53 provider-specific admission webhook registration and `Route53Identity` validation.
- `internal/providers/cloudflare/webhook`: Cloudflare provider-specific admission webhook registration and `CloudflareIdentity` validation.

Existing flat provider packages must be split during reconciliation when they contain more than one controller. A package named only by provider, such as `internal/providers/route53`, must not own reconciler entrypoints directly after the split. Provider root packages must not be kept as re-export facades for controller modules; manager wiring imports the controller modules directly.

Temporary pseudocode files are not part of the design source of truth. Controller behavior is defined by this document, the core API design, and the provider design documents.

## Acceptance Boundary

Claim `Accepted` is a two-stage result:

- Composition acceptance is decided by the Core ZoneUnit Controller before writing desired state to `ZoneUnit.spec`.
- Provider acceptance is decided by the provider controller after reading `ZoneUnit.spec`.
- The Core ZoneUnit Controller projects the composed result back to `Zone.status.conditions[Accepted]` and `RecordSet.status.conditions[Accepted]`.

Composition acceptance covers Kubernetes-side inputs: reference resolution, Provider and Provider storage-version resolution, provider version matching, namespace policy, `allowedZones`, `allowedRecordSets`, DNS ownership, CNAME exclusivity, and provider-payload validation and conversion from the Provider contract. Provider conversion webhook calls are part of this composition acceptance boundary because they convert inline payload before `ZoneUnit.spec` is written.

Provider acceptance covers provider-side or identity-dependent preflight policy that cannot be concluded by core alone but still describes whether the Kubernetes desired state is acceptable before external-state convergence starts. Examples include invalid provider identity references, provider account or plan restrictions known before operating the target resource, and unsupported provider contracts for that controller implementation.

Provider API observation and external-state reconciliation failures are not `Accepted=False`. If an adoption target does not exist, an adoption target differs from the desired state, a same-name external resource blocks creation, provider permissions fail, the provider API rejects the request, or the provider is unavailable, the accepted desired state is reported as `Programmed=False` with the corresponding reason. This keeps `Accepted` focused on whether dns-api can accept the manifest and provider contract, while `Programmed` reports whether the accepted desired state could be reflected to the external provider.

If composition acceptance is `False`, the claim `Accepted` is `False` and the item is not written to `ZoneUnit.spec`. If composition acceptance is `Unknown`, the claim `Accepted` is `Unknown` and the controller waits for the missing material. If composition acceptance is `True`, the claim `Accepted` follows provider acceptance from `ZoneUnit.status`.

`Accepted=False` means the controller has enough information to reject the claim. `Accepted=Unknown` means the validation material is incomplete or temporarily unavailable, such as missing `Zone`, missing `ZoneClass`, missing provider identity state, missing owner state during GitOps recovery, unavailable Provider storage version, unavailable Provider conversion webhook, or a transient core error.

## Withdrawal Composition and Projection

Core composes independent `RecordWithdrawal` claims into `ZoneUnit.spec.withdrawals[]`; providers do not read those claims, waivers, EndpointRecordSets, or platform kinds. Core alone writes public withdrawal/waiver status. The Endpoint App only creates retained pair claims; source Apps only preserve endpoint retirement intent.

Composition resolves the live Zone, Class UID/generation, served input and storage Provider versions, and exact current nondeleting Unit binding. Both input and storage versions must support `recordWithdrawal`. Core applies `allowedZones` and the corresponding A/AAAA `allowedRecordSets` grants based on address family. Cross-namespace access and wildcard access require explicit grants. Withdrawals reserve no DNS record ownership and do not conflict with replacement publication.

Acceptance is `True/Accepted` or `False/InvalidWithdrawal`, `NotAllowed`, or `UnsupportedProvider`; unlike ordinary unresolved claim acceptance, an unavailable withdrawal binding is `False/InvalidWithdrawal`. Valid-shaped rejected claims remain in the Unit with `allowed=false`. If the Zone inventory exceeds 256 claims, Core rejects the inventory as a whole, retains previous Unit entries disallowed with waivers removed, and never truncates obligations.

Core validates waiver claim UID, immutable-spec digest, canonical address, and namespace scope directly before attachment; deterministic namespace/name ordering selects the first matching waiver. An independent waiver reconciler maintains `Accepted` even if the claim or Zone disappears. No SubjectAccessReview, client-supplied audit identity, or receiving platform-resource lookup is involved.

Projection requires current Unit `status.observedGeneration`, Class UID/generation, exact current desired item including authorization/waiver, matching claim keys/UID/generation, and current `Withdrawn`/`Ready` condition generations. It preserves provider messages and public evidence, never private provider state. Override projection additionally requires the exact composed waiver name/UID/digest/reservation reference and current `OverrideAccepted=True/OperatorAuthorizedRelease`. Missing or stale results yield `Withdrawn=Unknown`, `Ready=False`, reason `DNSProbeFailed`, message `No current identity-bound withdrawal observation is available`. Same-name recreation cannot inherit success.

Core retains every withdrawal as a Zone deletion dependency, including verified claims, and continues composition/verification while the Zone waits for explicit evidence archival. Pending withdrawal status does not change unrelated Zone/RecordSet `Programmed`.

## Admission Webhook

Admission webhook is the API input boundary. It validates core schema, immutable fields, object-local references, object-local policy shape, and provider schema / CEL validation. It does not call DNS provider APIs, evaluate cross-object namespace / record access policy, or update status.

The initial implementation provides only validating webhooks. Mutating webhooks are not used. Defaults are handled by CRD schema defaults or controller-side omitted-field interpretation.

Webhook runs in-cluster. In development, `task up` prepares the kind cluster, dependencies, CRDs, controller Deployment, webhook Service, and `ValidatingWebhookConfiguration`. kest and CI use the same in-cluster deployment model. There is no development mode where the controller runs as a host-side process.

The webhook server runs as part of the controller-runtime manager and listens on `:9443` inside the Pod. The Kubernetes API server calls it through Service `dns-api-system/dns-api-webhook-service:443`. The Service `targetPort` is `9443`.

Webhook TLS certificates are issued by cert-manager. Tilt installs cert-manager as a Helm chart. dns-api manifests apply `Certificate`, `Issuer`, and `ValidatingWebhookConfiguration`. cert-manager CA injection sets the webhook `caBundle`.

Core validating webhook covers spec create/update and status update for `RecordWithdrawal` and `RecordWithdrawalWaiver` in addition to `Provider`, `ZoneClass`, `Zone`, `RecordSet`, and `ZoneUnit` as applicable. Withdrawal admission validates canonical relative name/address, required address-label consistency, no ownerReferences/finalizers, immutable spec, reference/digest shapes, and bounded conditions/evidence. It performs no DNS queries or cross-object grant checks. Unit withdrawal map keys must match nested claim identity; same-incarnation immutable shape cannot change. Status validates generation, the closed withdrawal reason table, 2048-byte messages, and public provider data against `recordWithdrawal.schemas.statusProviderData`; claim `provider.state` is forbidden. Provider-specific validating webhooks cover enabled resources such as `Route53Identity` and `CloudflareIdentity`; external has no identity handler. Core does not import provider API packages.

Provider controllers do not fully trust the webhook. The webhook is the primary user feedback boundary, but controllers keep minimal safety checks. Controller-side validation failure is returned as `Accepted=False` or `Programmed=False`.

`Zone.spec.domainName` is not normalized by the webhook; only canonical input is accepted. `RecordSet.spec.name` validation is type-dependent, so it is implemented in core webhook logic rather than CEL.

`RecordSet.spec.a.addresses`, `RecordSet.spec.aaaa.addresses`, `RecordSet.spec.txt.values`, `RecordSet.spec.cname.target`, `RecordSet.spec.mx.records`, `RecordSet.spec.caa.records`, and `RecordSet.spec.ns.nameServers` are not rewritten by the webhook. The webhook validates address family, parseability, TXT value length, TXT printable ASCII content, CNAME target syntax, MX preference range, MX exchange syntax, CAA flags, CAA tag grammar, delegated NS owner names, delegated name server syntax, semantic duplicates, and type/body combinations.

Status subresource validation checks common status fields, parent references, and provider data. For `Zone/status`, it validates `status.observedGeneration`, `status.nameServers`, `status.conditions`, and `status.provider.data`. For `RecordSet/status`, it validates `status.observedGeneration`, `status.zone.ref`, `status.conditions`, and `status.provider.data`. For `ZoneUnit/status`, it validates `status.observedGeneration`, resource-level conditions, `status.zone`, `status.recordSets[]`, `provider.data`, and `provider.state`. Public provider data is validated against the Provider storage version using `zone.schemas.statusProviderData` or `recordSet.schemas.statusProviderData`. `provider.state` is validated only for structural JSON shape unless the Provider version declares a provider-state schema. Conditions are validated against core reason combinations plus Provider version `additionalConditionReasons`.

For claim status, `status.observedGeneration` must not be greater than the admitted object's `metadata.generation`. This applies to `Zone/status` and `RecordSet/status`. `RecordSet.status.zone.ref`, when present, must contain only `namespace` and `name`, and must equal `spec.zoneRef` after defaulting an omitted `spec.zoneRef.namespace` to the `RecordSet` namespace. `RecordSet.status.zone.controllerName` is not accepted; controller identity is not a `RecordSet.status` field.

GitOps may apply `Zone` before its referenced `ZoneClass`. In that case, the webhook still validates `Zone.spec.provider`, `spec.adoption`, and provider CEL rules using the referenced Provider version. The webhook does not reject the `Zone` because `ZoneClass` is missing, `Zone.spec.provider` differs from `ZoneClass.spec.provider`, or the current namespace labels do not match `ZoneClass.spec.allowedZones`. Reference-dependent validation is returned later by the controller as `Accepted=Unknown` or `Accepted=False`.

GitOps may apply `RecordSet` before its referenced `Zone` or `ZoneClass`. In that case, the webhook still validates `RecordSet.spec.provider`, `spec.options`, `spec.adoption`, `spec.type`, record body shape, and provider CEL rules using the referenced Provider version. The webhook does not reject the `RecordSet` because `Zone` or `ZoneClass` is missing, `RecordSet.spec.provider` differs from `ZoneClass.spec.provider`, or the current namespace labels / record policy do not match `Zone.spec.allowedRecordSets`. Reference-dependent validation is returned later by the controller as `Accepted=Unknown` or `Accepted=False`.

Provider version resolution is a shared helper. It gets `Provider/provider.name`, and returns the served version whose `spec.versions[].name` matches `provider.version`. If the Provider does not exist, the version does not exist, `served=false`, or the target kind schema is missing, create/update admission rejects the request with `InvalidProvider`. Provider resources are installation-time infrastructure, not ordinary GitOps claim resources, so a missing `Provider` is rejected at admission.

Provider validation order:

```python
validate Provider:
  require spec.display.name
  require spec.versions
  require every spec.versions item has name
  require exactly one spec.versions item has storage=true
  require every spec.versions item has recordSet.supportedTypes
  require every supportedTypes item is one of A, AAAA, TXT, CNAME, MX, CAA, NS
  reject duplicate supportedTypes entries in the same version
  validate zone.additionalConditionReasons conditionType, status, reason, and description
  validate recordSet.additionalConditionReasons conditionType, status, reason, and description
  reject additionalConditionReasons that duplicate core reason combinations
  reject duplicate additionalConditionReasons in the same version and target kind
  validate schemas are valid OpenAPI v3 fragments
  validate zoneClass.validationRules[].rule compiles with ZoneClass self and returns bool
  validate zone.validationRules[].rule compiles with Zone self and returns bool
  validate recordSet.validationRules[].rule compiles with RecordSet self and returns bool
  validate recordSet.disableValidations[].when compiles with RecordSet self and returns bool
  allow recordSet.disableValidations[].name values require-ttl, require-a-addresses, require-aaaa-addresses, require-txt-values, forbid-cname-apex, forbid-txt-non-printable-ascii
  validate conversion.toStorage.cel compiles with the target provider payload and returns object
```

ZoneClass validation order:

```python
validate ZoneClass:
  require spec.provider.name
  require spec.provider.version
  require spec.controllerName
  require spec.controllerName has a DNS subdomain prefix, "/", and non-empty path

  provider, providerVersion = resolve ProviderVersion(spec.provider)

  if providerVersion.identity is present:
    require spec.identityRef with nonempty name
    validate spec.identityRef shape by providerVersion.identity.resource
  else:
    reject spec.identityRef
  validate spec.parameters by providerVersion.zoneClass.schemas.parameters
  validate providerVersion.zoneClass.validationRules
  validate spec.allowedZones
```

Zone validation order:

```python
validate Zone:
  require spec.domainName
  require spec.zoneClassRef
  require spec.provider

  provider, providerVersion = resolve ProviderVersion(spec.provider)

  validate spec.adoption by providerVersion.zone.schemas.adoption
  validate providerVersion.zone.validationRules
  validate spec.domainName syntax
  validate spec.allowedRecordSets

  zoneClass = get ZoneClass(spec.zoneClassRef)

  if zoneClass is found:
    do not reject spec.provider mismatch; controllers report ProviderMismatch in status
    do not reject zone namespace policy mismatch; controllers report NotAllowedByZoneClass in status
```

RecordSet validation order:

```python
validate RecordSet:
  require spec.zoneRef
  require spec.provider
  require spec.type
  require spec.name

  provider, providerVersion = resolve ProviderVersion(spec.provider)

  require spec.type in providerVersion.recordSet.supportedTypes
  validate spec.options by providerVersion.recordSet.schemas.options
  validate spec.adoption by providerVersion.recordSet.schemas.adoption
  disabled = evaluate providerVersion.recordSet.disableValidations
  validate record name syntax by spec.type and spec.name

  unless disabled("require-ttl"):
    require spec.ttl
  if spec.type == "A":
    reject spec.aaaa
    reject spec.txt
    reject spec.cname
    reject spec.mx
    reject spec.caa
    reject spec.ns
    if not disabled("require-a-addresses"):
      require spec.a.addresses
      require every spec.a.addresses item parses as IPv4
      require no duplicate parsed IPv4 address
  if spec.type == "AAAA":
    reject spec.a
    reject spec.txt
    reject spec.cname
    reject spec.mx
    reject spec.caa
    reject spec.ns
    if not disabled("require-aaaa-addresses"):
      require spec.aaaa.addresses
      require every spec.aaaa.addresses item parses as IPv6
      require no duplicate parsed IPv6 address
  if spec.type == "TXT":
    reject spec.a
    reject spec.aaaa
    reject spec.cname
    reject spec.mx
    reject spec.caa
    reject spec.ns
    if not disabled("require-txt-values"):
      require spec.txt.values
      require every spec.txt.values item is non-empty
      require every spec.txt.values item is 1..4000 UTF-8 octets
      require no duplicate spec.txt.values item
      if not disabled("forbid-txt-non-printable-ascii"):
        require every spec.txt.values item contains only printable ASCII bytes 0x20..0x7e
  if spec.type == "CNAME":
    if not disabled("forbid-cname-apex"):
      reject spec.name == "@"
    require spec.cname.target
    require spec.cname.target is canonical lowercase ASCII DNS name without trailing root dot
    require spec.cname.target is not an IP address, URL, or URI
    reject spec.a
    reject spec.aaaa
    reject spec.txt
    reject spec.mx
    reject spec.caa
    reject spec.ns
  if spec.type == "MX":
    require spec.mx.records
    require spec.mx.records is not empty
    require every spec.mx.records item has preference in range 0..65535
    require every spec.mx.records item has exchange
    if any exchange == ".":
      require exactly one spec.mx.records item
      require that item has preference 0
    require every exchange except "." is canonical lowercase ASCII DNS name without trailing root dot
    require every exchange except "." is not an IP address, URL, or URI
    require no duplicate preference and exchange pair
    reject spec.a
    reject spec.aaaa
    reject spec.txt
    reject spec.cname
    reject spec.caa
    reject spec.ns
  if spec.type == "CAA":
    require spec.caa.records
    require spec.caa.records is not empty
    require every spec.caa.records item has flags in range 0..255
    require every spec.caa.records item has tag matching lowercase ASCII alphanumeric CAA tag grammar
    require every spec.caa.records item has non-empty value
    require no duplicate flags, tag, and value tuple
    reject spec.a
    reject spec.aaaa
    reject spec.txt
    reject spec.cname
    reject spec.mx
    reject spec.ns
  if spec.type == "NS":
    reject spec.name == "@"
    reject spec.name is wildcard name
    require spec.ns.nameServers
    require spec.ns.nameServers is not empty
    require every spec.ns.nameServers item is canonical lowercase ASCII DNS name without trailing root dot
    require every spec.ns.nameServers item is not an IP address, URL, or URI
    require no duplicate spec.ns.nameServers item
    reject spec.a
    reject spec.aaaa
    reject spec.txt
    reject spec.cname
    reject spec.mx
    reject spec.caa

  validate providerVersion.recordSet.validationRules

  zone = get Zone(spec.zoneRef)
  zoneClass = get ZoneClass(zone.spec.zoneClassRef) if zone is found

  if zone and zoneClass are found:
    do not reject spec.provider mismatch; controllers report ProviderMismatch in status
    do not reject record access policy mismatch; controllers report NotAllowedByZone in status
    validate FQDN length by spec.name and zone.spec.domainName
```

Status validation order:

```python
validate Zone/status:
  if status.observedGeneration is present:
    require status.observedGeneration <= metadata.generation
  validate status.conditions
  validate status.nameServers
  validate status.provider.data by ProviderVersion(zone.spec.provider).zone.schemas.statusProviderData

validate RecordSet/status:
  if status.observedGeneration is present:
    require status.observedGeneration <= metadata.generation
  if status.zone.ref is present:
    require status.zone.ref.name == spec.zoneRef.name
    require status.zone.ref.namespace == default(spec.zoneRef.namespace, metadata.namespace)
    reject any status.zone.controllerName field
  validate status.conditions
  validate status.provider.data by ProviderVersion(recordSet.spec.provider).recordSet.schemas.statusProviderData
```
