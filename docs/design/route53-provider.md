# Route 53 Provider

## Route53Identity

`Route53Identity` is a namespace-scoped provider-specific resource. It represents AWS account, AWS region, credential source, and optional assume role chain used by Route 53 controller operations. `Route53Identity` lives in the same namespace as the `ZoneClass` that references it.

`Route53Identity.spec` fields:

- `accountID`: final AWS account ID that this identity operates. The controller resolves credentials and calls STS `GetCallerIdentity`; returned account ID must match.
- `region`: AWS region used for SDK config and STS endpoint resolution. Required. Route 53 public hosted zones are global, but WebIdentity credentials, `AssumeRole`, `GetCallerIdentity`, and Route 53 client endpoint resolution use this region.
- `credentials`: credential source union. Initially only `runtime` is accepted.
- `credentials.runtime`: read the base credential from the AWS SDK default credential chain in the controller runtime environment.
- `assumeRoleChain`: array of IAM roles to assume sequentially from the base credential. If omitted, use the base credential directly.
- `assumeRoleChain[].roleARN`: IAM role ARN to assume.
- `assumeRoleChain[].externalID`: optional external ID passed to STS `AssumeRole`.
- `assumeRoleChain[].sessionName`: STS session name. If omitted, the controller generates a stable value.

`Route53Identity.status` contains:

- `observedGeneration`.
- `accountID`: account ID returned by STS for the final credential.
- `conditions`: `Accepted` and `Ready`.
- `lastCredentialCheckTime`: last live credential check time.
- `nextCredentialCheckTime`: approximate next scheduled live check time. Empty means no automatic retry is scheduled.

`Accepted=True` means the spec can be statically accepted: region shape, credential source, assume role chain shape, and policy. `Ready=True` means the controller has successfully resolved credentials and confirmed the final AWS account ID with STS.

`Accepted=False` reasons:

- `InvalidRegion`: region is invalid for AWS SDK / STS usage.
- `InvalidCredentialSource`: unsupported or invalid credential source.
- `InvalidAssumeRoleChain`: assume role chain is invalid.
- `DeniedByPolicy`: platform policy rejects this identity.

`Ready=False` reasons:

- `CredentialUnavailable`: runtime credential cannot be resolved.
- `AssumeRoleFailed`: one assume role step failed.
- `AccountMismatch`: final STS account ID does not match `spec.accountID`.
- `ProviderAccessDenied`: STS permission failure.
- `ProviderUnavailable`: AWS STS outage, rate limit, or temporary failure.
- `ReconcileError`: other controller internal error.

`lastCredentialCheckTime` is set whenever a live check finishes, whether success or failure. `nextCredentialCheckTime` is set when the controller schedules a retry. It is diagnostic and approximate; Kubernetes watch events, controller restart, spec updates, or leader changes may reconcile earlier. It is empty when no automatic retry is scheduled.

`Accepted=False` reasons `InvalidRegion`, `InvalidCredentialSource`, `InvalidAssumeRoleChain`, `DeniedByPolicy`, and `Ready=False` reason `AccountMismatch` usually require spec, policy, or runtime credential changes, so they are not retried on a short loop. They may be re-evaluated on resource update, controller restart, or controller-runtime resync, but `nextCredentialCheckTime` does not show short retry for them.

Example:

```yaml
apiVersion: route53.dns.appthrust.io/v1alpha1
kind: Route53Identity
metadata:
  namespace: tenant-a-platform
  name: route53-dev
spec:
  accountID: "123456789012"
  region: ap-northeast-1
  credentials:
    runtime: {}
```

Multiple-account example:

```yaml
apiVersion: route53.dns.appthrust.io/v1alpha1
kind: Route53Identity
metadata:
  namespace: tenant-a-platform
  name: account-b
spec:
  accountID: "222222222222"
  region: ap-northeast-1
  credentials:
    runtime: {}
  assumeRoleChain:
    - roleARN: arn:aws:iam::222222222222:role/dns-api-route53
      sessionName: dns-api-route53
```

### Route53Identity Events

Route53Identity Events are diagnostic. They do not replace status.

Common Events:

| type | reason | related | When emitted |
| --- | --- | --- | --- |
| `Normal` | `Accepted` | none | `Accepted=True`. |
| `Warning` | `InvalidRegion` | none | static region validation fails. |
| `Warning` | `InvalidCredentialSource` | none | credential source validation fails. |
| `Warning` | `InvalidAssumeRoleChain` | none | assume role chain validation fails. |
| `Warning` | `DeniedByPolicy` | none | platform policy rejects the identity. |
| `Normal` | `Ready` | none | live credential check succeeds. |
| `Warning` | `CredentialUnavailable` | none | runtime credential cannot be resolved. |
| `Warning` | `AssumeRoleFailed` | none | assume role chain fails. |
| `Warning` | `AccountMismatch` | none | final account ID does not match `spec.accountID`. |
| `Warning` | `ProviderAccessDenied` | none | STS permission failure. |
| `Warning` | `ProviderUnavailable` | none | AWS STS outage, rate limit, or temporary failure. |
| `Warning` | `ReconcileError` | none | controller internal error. |

### Controller Responsibility

Route 53 controller reconciles `Route53Identity`. It validates the spec, resolves runtime credentials, applies the assume role chain, calls STS `GetCallerIdentity`, compares account IDs, and updates status. It never stores credential material in status or events.

## Route 53 Provider

Route 53 provider schema is represented by `Provider/route53.dns.appthrust.io` version `v1alpha1`. Route 53 `ZoneClass` configuration is stored in `ZoneClass.spec.parameters`. `ZoneClass.spec.provider.name` and claim `spec.provider.name` reference `route53.dns.appthrust.io`; their `version` fields reference the served Provider version.

Route 53 provider handles Public DNS only. Hosted zones created by the Route 53 controller are public hosted zones. VPC settings for private hosted zones are not stored in `ZoneClass` or `Zone`.

Route 53 identity selection uses `ZoneClass.spec.identityRef.name`. The selected identity resource is a `Route53Identity` in the same namespace as the `ZoneClass`.

Route 53 `ZoneClass.spec.parameters` fields:

- `zoneCreationPolicy`: whether new hosted zone creation is allowed. Values: `Create` or `Deny`. Default: `Create`.
- `zoneDeletionPolicy`: whether the hosted zone is deleted when the `Zone` is deleted. Values: `Delete` or `Retain`. Default: `Retain`.
- `sameNameZonePolicy`: how to handle existing same-name hosted zones in the Route 53 account. Values: `Allow` or `Deny`. Default: `Deny`.
- `tags`: Route 53 tags added to created hosted zones.

Route 53 `Provider` schema descriptions for `zoneClass.schemas.parameters`:

| Field | `description` |
| --- | --- |
| `zoneCreationPolicy` | `Controls whether dns-api may create a Route 53 public hosted zone when a Zone does not adopt an existing hosted zone.` |
| `zoneDeletionPolicy` | `Controls whether dns-api deletes the Route 53 hosted zone when the Kubernetes Zone is deleted.` |
| `sameNameZonePolicy` | `Controls whether dns-api may create a new Route 53 hosted zone when another same-name hosted zone already exists.` |
| `tags` | `Additional Route 53 tags applied to hosted zones created by this ZoneClass.` |
| `tags.<key>` | `One Route 53 tag value applied to hosted zones created by this ZoneClass.` |

AWS credentials themselves are not stored in `ZoneClass.spec.parameters`. `ZoneClass.spec.identityRef` selects the AWS account and assume role chain through `Route53Identity`.

### Controller Responsibility

Route 53 ZoneClass controller reconciles `ZoneClass` resources whose `spec.controllerName` matches the Route 53 controller name and whose `spec.provider.name` is `route53.dns.appthrust.io`. It interprets `ZoneClass.spec.identityRef` as a `Route53Identity` reference, interprets `ZoneClass.spec.parameters` as Route 53 schema and policy, checks that the identity can be statically resolved, and updates `ZoneClass.status.conditions[Accepted]`. It does not call hosted zone APIs.

Route 53 ZoneUnit controller reconciles `ZoneUnit` resources whose `spec.zone.zoneClassRef` resolves to a matching Route 53 `ZoneClass`. After confirming the referenced `Route53Identity` is `Ready=True`, it creates, adopts, deletes, and updates hosted zones; reconciles child record sets to Route 53; and updates `ZoneUnit.status`. It does not read or write `Zone` or `RecordSet` claims.

If `identityRef` cannot be resolved or the referenced `Route53Identity` is not resolved enough to decide, the Route 53 controller does not proceed with hosted zone reconciliation and sets `ZoneUnit.status.zone.conditions[Accepted]` to `Unknown`, reason `IdentityNotResolved`. If the referenced `Route53Identity` is `Accepted=False`, it sets `Accepted=False`, reason `InvalidIdentityRef`.

If the referenced `Route53Identity` is `Accepted=True` but not `Ready=True`, the Route 53 controller does not proceed with hosted zone or record-set reconciliation. It sets `ZoneUnit.status.zone.conditions[Programmed]` and accepted `ZoneUnit.status.recordSets[].conditions[Programmed]` to `False`, reason `ProviderIdentityNotReady`. Projected claim status messages do not include detailed AWS identity information. Details are returned on `Route53Identity.status`.

`zoneDeletionPolicy` is Route 53 provider deletion policy. Core `Zone.spec` does not contain deletion policy.

When `zoneDeletionPolicy=Retain`, deleting the core `Zone` does not delete the Route 53 hosted zone. The Route 53 ZoneUnit controller removes its provider `ZoneUnit` finalizer after recording cleanup completion. If policy changes from `Delete` to `Retain` while the `ZoneUnit` is not being deleted, the controller does not start provider deletion.

When `zoneDeletionPolicy=Delete`, the Route 53 ZoneUnit controller keeps a provider finalizer on `ZoneUnit`. When `ZoneUnit.metadata.deletionTimestamp` is set, it deletes the Route 53 hosted zone, then removes the provider finalizer after deletion completes.

If Route 53 rejects hosted zone deletion, the controller keeps the finalizer and sets `Programmed=False`, reason `ProviderConflict`. For example, non-default record sets remaining in the hosted zone are treated this way.

When `Zone.spec.adoption` is absent, the first implementation milestone creates public hosted zones only when `zoneCreationPolicy=Create`. If `Zone.spec.adoption` is absent and `zoneCreationPolicy=Deny`, the Route 53 controller sets `Zone Accepted=False`, reason `DeniedByPolicy`. `zoneCreationPolicy=Deny` is for adoption-only classes.

`sameNameZonePolicy` applies only to creation when `Zone.spec.adoption` is absent. With `Deny`, an existing same-name public hosted zone results in `Programmed=False`, reason `ProviderConflict`. With `Allow`, a new hosted zone may be created even when a same-name zone exists. To manage an existing hosted zone, use `Zone.spec.adoption`.

Route 53 controller stores `CreateHostedZone` `CallerReference` only as provider-internal state in `ZoneUnit.status.provider.state`. It is not stored in `ZoneUnit.spec` and is not public `status.provider.data`. The controller may reuse a cached caller reference while status is present, but the caller reference is not the recovery source of truth.

If Route 53 status is lost after a hosted zone was created, recovery uses explicit adoption through `Zone.spec.adoption.hostedZoneId` / `ZoneUnit.spec.zone.adoption.hostedZoneId`. If status and adoption are both absent, the controller does not automatically adopt a same-name hosted zone. It reports `Programmed=False`, reason `ProviderConflict`, when provider state is ambiguous.

Temporary Route 53 ZoneUnit controller pseudocode is not part of the design source of truth. Hosted zone resolution order, finalizers, pending changes, Route 53 API errors, batch limits, and requeue behavior are defined in this document.

Route 53 controller re-observes hosted zones and child record sets even in stable state. If provider state differs from `ZoneUnit.spec`, it treats the difference as drift and self-heals owned items. Re-observation intervals, change check intervals, and temporary error retry intervals are controller configuration, not `Zone.spec` or `RecordSet.spec`.

Route 53 controller also enqueues a `ZoneUnit` when `ZoneUnit.metadata.annotations["dns.appthrust.io/reconcile-request"]` changes. This requests re-observation of the hosted zone and child record sets without waiting for periodic re-observation. It follows the same validation, pending change, batch serialization, and drift self-healing rules. Updating the annotation alone must not set `Programmed=False` or send no-op provider API changes when external state already matches `ZoneUnit.spec`.

Route 53 `Zone.spec.adoption` shape:

```yaml
spec:
  adoption:
    hostedZoneId: Z10219583PPQMV0U1KGN2
```

`hostedZoneId` is a Route 53 hosted zone ID in `Z...` form. `/hostedzone/Z...` and hosted zone ARNs are not accepted. This shape is validated by Route 53 Provider version `zone.schemas.adoption`.

Route 53 `Provider` schema descriptions for `zone.schemas.adoption`:

| Field | `description` |
| --- | --- |
| `hostedZoneId` | `Route 53 public hosted zone ID of the existing hosted zone to adopt.` |

When adoption is specified, the Route 53 controller gets the hosted zone, verifies that the hosted zone name matches `ZoneUnit.spec.zone.domainName`, and verifies that it is public. If it matches, the controller manages it and stores `hostedZoneId` in `ZoneUnit.status.zone.provider.data.hostedZoneID`. If the hosted zone does not exist, set `Programmed=False`, reason `ExternalResourceNotFound`. If it is private or the name differs, set `Programmed=False`, reason `ExternalResourceMismatch`.

Created hosted zones receive ownership tags:

- `appthrust.io/managed-by`: `dns-api`
- `appthrust.io/zone-namespace`: `Zone` namespace
- `appthrust.io/zone-name`: `Zone` name
- `appthrust.io/zone-class-namespace`: `ZoneClass` namespace
- `appthrust.io/zone-class-name`: `ZoneClass` name

`ZoneClass.spec.parameters.tags` are additional tags separate from ownership tags. Route 53 controller applies both ownership tags and `ZoneClass.spec.parameters.tags`.

Ownership tag keys are reserved and managed by the controller. `ZoneClass.spec.parameters.tags` must not use the same keys. If a reserved key is specified, the controller sets `Accepted=False`, reason `DeniedByPolicy`.

Test tags are also specified in `ZoneClass.spec.parameters.tags`. kest uses `appthrust.io/test-scope=kest` and `appthrust.io/test-id=<generated>`.

The initial milestone does not support shared ownership of hosted zones. Ownership tag naming assumes one platform. Shared ownership will require a later design revision.

Route 53-specific public hosted zone data is stored in `ZoneUnit.status.zone.provider.data` and projected to `Zone.status.provider.data`, validated by Route 53 Provider version `zone.schemas.statusProviderData`. `hostedZoneID` is public because users can copy it into adoption settings, use it to open the hosted zone in the AWS dashboard, and include it in support or troubleshooting workflows.

Route 53 `Zone.status.provider.data` fields:

- `hostedZoneID`: Route 53 hosted zone ID in `Z...` form.

Route 53 `Provider` schema descriptions for `zone.schemas.statusProviderData`:

| Field | `description` |
| --- | --- |
| `hostedZoneID` | `Route 53 hosted zone ID managed for this Zone.` |

Pending Route 53 hosted-zone and record-set changes are stored in `ZoneUnit.status.provider.state`, not in public claim status. Pending fields are kept only while Route 53 reports `PENDING`. Completed `INSYNC` changes are not retained as history. `Programmed=True` is set after pending fields are removed and provider state is re-observed to match `ZoneUnit.spec`.

Route 53-specific Zone Events do not replace common Events. They provide operation-level diagnostics for Route 53 API actions. Their reasons start with `Route53`. Messages may include `domainName`, hosted zone ID, change ID, and operation. They do not include AWS account ID, role ARN, assume role chain, access keys, session tokens, or raw AWS SDK error bodies.

Route 53-specific Zone Events:

| type | reason | related | When emitted |
| --- | --- | --- | --- |
| `Normal` | `Route53HostedZoneChangeSubmitted` | none | `CreateHostedZone` or `DeleteHostedZone` change submitted. |
| `Normal` | `Route53HostedZoneChangeInSync` | none | Route 53 hosted zone create/delete change becomes `INSYNC`. |

## Route 53 RecordSet Provider Schema

Route 53 ALIAS is created as an A or AAAA resource record set in the Route 53 API. dns-api represents it as `type=A` or `type=AAAA` with provider-specific data in `spec.options.alias`.

Initial Route 53 provider schema creates, updates, and adopts standard A records, standard AAAA records, standard TXT records, standard CNAME records, standard MX records, standard CAA records, delegated NS records, and Route 53 ALIAS. Standard A uses `type=A`, `spec.ttl`, and `spec.a.addresses`. Standard AAAA uses `type=AAAA`, `spec.ttl`, and `spec.aaaa.addresses`. Standard TXT uses `type=TXT`, `spec.ttl`, and `spec.txt.values`. Standard CNAME uses `type=CNAME`, `spec.ttl`, and `spec.cname.target`. Standard MX uses `type=MX`, `spec.ttl`, and `spec.mx.records`. Standard CAA uses `type=CAA`, `spec.ttl`, and `spec.caa.records`. Delegated NS uses `type=NS`, `spec.ttl`, and `spec.ns.nameServers`. Route 53 ALIAS uses `type=A` or `type=AAAA` and `spec.options.alias`, without standard address bodies. Standard `SRV` reconciliation is out of initial scope.

Route 53 ALIAS example:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: RecordSet
metadata:
  namespace: tenant-a-app
  name: root-alias
spec:
  zoneRef:
    name: example-com
  provider:
    name: route53.dns.appthrust.io
    version: v1alpha1
  type: A
  name: "@"
  options:
    alias:
      dnsName: dualstack.example-alb.ap-northeast-1.elb.amazonaws.com.
      hostedZoneID: Z14GRHDCWA56QT
      evaluateTargetHealth: true
```

For standard AAAA records, the controller parses each `spec.aaaa.addresses` value as an IPv6 address and also parses observed Route 53 `ResourceRecords` values as IPv6 addresses before comparing. Textual IPv6 representation differences alone must not be treated as drift. The controller may send either the user-provided string or a canonical string to Route 53, but dns-api spec is not rewritten.

For standard TXT records, the controller converts each `spec.txt.values` item into Route 53 `ResourceRecords[].Value`. It quotes TXT values for Route 53 and splits one logical TXT value into multiple quoted chunks of at most 255 octets when needed. Users do not write quoted strings or chunk arrays in `spec.txt.values`. Observed Route 53 TXT RRsets are parsed from quoted chunks back into logical TXT values and compared to `spec.txt.values` as unordered sets. Quote, escape, and chunk-boundary differences alone must not be treated as drift.

Route 53 `v1alpha1` does not disable the core `forbid-txt-non-printable-ascii` validation. `spec.txt.values[]` values containing newline, tab, other control characters, or non-ASCII characters are rejected by the core webhook before Route 53 reconciliation. The Route 53 controller also keeps the same defensive check before building an API change batch. Route 53 supports octal escape notation for some TXT characters, but dns-api `v1alpha1` does not expose an octal-escaped TXT input contract or non-ASCII round-trip behavior. A future Provider version may disable the core validation only after it defines provider-specific escape, unescape, display, and comparison behavior.

For standard CNAME records, the controller converts `spec.cname.target` into one Route 53 `ResourceRecords[].Value` item. The value sent to Route 53 has a trailing root dot. Observed Route 53 CNAME values are compared to `spec.cname.target` after normalizing the trailing root dot and provider-specific escaping. A CNAME RRSet must have exactly one value. If Route 53 returns zero values or more than one value for an existing CNAME RRSet, the controller treats it as provider drift or mismatch and drives it back to the single target in `spec.cname.target` when the item exists in `ZoneUnit.spec.recordSets[]`.

For standard MX records, the controller converts each `spec.mx.records` item into one Route 53 `ResourceRecords[].Value` string in `<preference> <exchange>.` form, for example `10 mail1.example.net.`. Null MX is sent as `0 .`. Observed Route 53 MX values are parsed back into `(preference, exchange)` pairs and compared to `spec.mx.records` as unordered sets after normalizing trailing root dot differences and provider-specific escaping. If an observed MX value cannot be parsed into one integer preference and one DNS name or valid Null MX root exchange, the controller treats it as provider drift or mismatch and drives it back to `spec.mx.records` when the item exists in `ZoneUnit.spec.recordSets[]`.

For standard CAA records, the controller converts each `spec.caa.records` item into one Route 53 `ResourceRecords[].Value` string in `<flags> <tag> "<value>"` form, for example `0 issue "letsencrypt.org"`. Route 53 requires the CAA value part to be quoted. Observed Route 53 CAA values are parsed back into `(flags, tag, value)` tuples and compared to `spec.caa.records` as unordered sets after normalizing provider-specific quoting and escaping. If an observed CAA value cannot be parsed into one integer flags value, one tag, and one value, the controller treats it as provider drift or mismatch and drives it back to `spec.caa.records` when the item exists in `ZoneUnit.spec.recordSets[]`.

For delegated NS records, the controller converts each `spec.ns.nameServers` item into one Route 53 `ResourceRecords[].Value` string with a trailing root dot. Observed Route 53 NS values are compared to `spec.ns.nameServers` as unordered sets after normalizing trailing root dot differences and provider-specific escaping. The Route 53 controller does not manage the current hosted zone's apex NS RRSet as a `RecordSet`; Route 53 assigned apex name servers are observed through `Zone.status.nameServers`.

For Route 53 ALIAS, users do not specify TTL or `ResourceRecords`, so `spec.ttl`, `spec.a.addresses`, `spec.aaaa.addresses`, `spec.txt.values`, `spec.cname.target`, `spec.mx.records`, `spec.caa.records`, and `spec.ns.nameServers` must be absent. Route 53 `Provider` disables `require-ttl`, `require-a-addresses`, and `require-aaaa-addresses` when `spec.options.alias` exists, and uses CEL validation to reject simultaneous TTL or standard body fields.

`spec.options.alias.dnsName`, `spec.options.alias.hostedZoneID`, and `spec.options.alias.evaluateTargetHealth` are explicit desired state. For an alias to an AWS resource such as an Elastic Load Balancer, `hostedZoneID` is the alias target resource hosted zone ID, such as the load balancer `CanonicalHostedZoneId`, not the dns-api `Zone.status.provider.data.hostedZoneID`. The Route 53 controller does not discover alias target hosted zone IDs from Elastic Load Balancing, CloudFront, S3, API Gateway, Kubernetes `Service`, Kubernetes `Ingress`, or any other non-Route 53 resource. Users, platform automation, or IaC tools provide the alias target DNS name and hosted zone ID in the `RecordSet` manifest.

Route 53 `Provider` schema descriptions for `recordSet.schemas.options`:

| Field | `description` |
| --- | --- |
| `alias` | `Route 53 alias target for an A or AAAA RecordSet. Alias records omit TTL and standard record values.` |
| `alias.dnsName` | `DNS name of the Route 53 alias target. Use a trailing root dot.` |
| `alias.hostedZoneID` | `Hosted zone ID of the alias target resource, such as an Elastic Load Balancer CanonicalHostedZoneId.` |
| `alias.evaluateTargetHealth` | `Whether Route 53 evaluates target health for this alias record.` |

### Controller Responsibility

Route 53 does not have a separate Route 53 RecordSet controller. The Route 53 ZoneUnit controller reconciles all `ZoneUnit.spec.recordSets[]` items for a hosted zone. This matches Route 53 APIs: `ListResourceRecordSets` is hosted-zone-scoped and `ChangeResourceRecordSets` submits hosted-zone-scoped batches.

Route 53 ZoneUnit controller does not arbitrate claim ownership. The Core ZoneUnit Controller writes only accepted owner items to `ZoneUnit.spec.recordSets[]`. Route 53 applies changes to Route 53, updates `ZoneUnit.status`, and deletes Route 53 record sets when `recordSets[].deletionRequested=true`.

For Route 53 ALIAS, the Route 53 ZoneUnit controller validates and applies the explicit `spec.options.alias` object to Route 53 `AliasTarget`. It must not mutate the `RecordSet` spec or fill missing alias fields by calling other AWS service APIs. If the alias target DNS name or hosted zone ID is absent or invalid, validation fails through the provider schema or Route 53 API response and the controller reports the failure with the normal `Accepted` or `Programmed` condition contract.

For standard CNAME, the Route 53 ZoneUnit controller validates and applies `cname.target` as a normal Route 53 CNAME record. It does not use Route 53 `AliasTarget` and does not translate CNAME into Route 53 ALIAS. If Route 53 rejects a CNAME because it is at the zone apex or conflicts with another RRSet at the same name, the controller maps the failure to `Programmed=False`, reason `ProviderConflict`, unless core composition has already rejected the claim with `Accepted=False`.

For standard MX, the Route 53 ZoneUnit controller validates and applies `spec.mx.records` as a normal Route 53 MX record set. It does not use Route 53 `AliasTarget`. If Route 53 rejects an MX record because of provider-side conflict or invalid request, the controller maps the failure through the normal provider error contract.

For standard CAA, the Route 53 ZoneUnit controller validates and applies `spec.caa.records` as a normal Route 53 CAA record set. It does not use Route 53 `AliasTarget`. If Route 53 rejects a CAA record because of provider-side conflict or invalid request, the controller maps the failure through the normal provider error contract.

For delegated NS, the Route 53 ZoneUnit controller validates and applies `spec.ns.nameServers` as a normal Route 53 NS record set only when `spec.name` is not apex and not wildcard. It does not change the hosted zone's provider-assigned apex NS or SOA records. If Route 53 rejects a delegated NS record because of provider-side conflict or invalid request, the controller maps the failure through the normal provider error contract.

Before creating, adopting, or updating a Route 53 record set, the controller ensures the provider finalizer exists on the owning `ZoneUnit`.

Deletion target keys are not derived from `RecordSet.status`. During deletion, hosted zone ID, record name, and record type are computed from `ZoneUnit.spec.zone` and `ZoneUnit.spec.recordSets[]`.

Route 53 ZoneUnit controller serializes `ChangeResourceRecordSets` by hosted zone. Changes beyond Route 53 batch limits are deferred to a later reconcile. Deferred `RecordSet` resources get `Programmed=False`, reason `ProviderChangeDeferred`. The controller uses pending change checks, `ListResourceRecordSets` observation, batch splitting, and immediate requeue behavior to keep provider changes serialized and observable.

Route 53 controller self-heals. After `Programmed=True`, periodic re-observation or Kubernetes events may find that Route 53 RRSet state differs from `ZoneUnit.spec.recordSets[]`. The controller sets `Programmed=False` with `ProviderChangePending` or `ProviderChangeDeferred`, and drives the provider back to desired state. `Programmed=True` means not only that the last change became `INSYNC`, but also that the last observed Route 53 RRSet matches the accepted desired state and no pending change affects the record identity.

Route 53 APIs may return DNS names with escaped octal labels. For example, wildcard label `*` may be returned as `\052`. The controller normalizes observed Route 53 RRSet names into dns-api canonical record names before comparing. `*.platform2.test.` and `\052.platform2.test.` are the same record identity. Values sent to Route 53 may use any form accepted by Route 53, but dns-api spec, status, adoption payloads, `ZoneUnit`, and UI use canonical form.

Route 53-specific RecordSet Events do not replace common Events. They track Route 53 record set changes. Reasons start with `Route53`. Route 53 record set changes are submitted in hosted-zone batches, but Events are emitted per affected `RecordSet`, not on the `Zone`.

Route 53-specific RecordSet Events:

| type | reason | related | When emitted |
| --- | --- | --- | --- |
| `Normal` | `Route53RecordSetChangeSubmitted` | `Zone` | a `ChangeResourceRecordSets` change including the target `RecordSet` is submitted. |
| `Normal` | `Route53RecordSetChangeInSync` | `Zone` | the Route 53 record set change including the target `RecordSet` becomes `INSYNC`. |

Route 53 `RecordSet.spec.adoption` shape:

```yaml
spec:
  adoption:
    enabled: true
```

When `adoption.enabled=true`, Route 53 attempts to adopt the record set whose hosted zone, record type, and record name are derived from the referenced `Zone` and the `RecordSet` spec. Users do not repeat hosted zone ID, record type, or FQDN in adoption payload because those values are already known from the accepted `ZoneUnit.spec`.

Route 53 `Provider` schema descriptions for `recordSet.schemas.adoption`:

| Field | `description` |
| --- | --- |
| `enabled` | `Whether dns-api may adopt an existing Route 53 record set matching the accepted Zone and RecordSet desired state.` |

If the derived Route 53 record set exists and its TTL and values, or `AliasTarget`, match the accepted desired state, the controller adopts it. For CNAME, matching values means the observed Route 53 RRSet has exactly one CNAME value and it equals `cname.target` after DNS-name normalization. For MX, matching values means the observed Route 53 RRSet parses to the same unordered set of `(preference, exchange)` pairs as `mx.records`. For CAA, matching values means the observed Route 53 RRSet parses to the same unordered set of `(flags, tag, value)` tuples as `caa.records`. For delegated NS, matching values means the observed Route 53 RRSet has the same unordered set of name server DNS names as `ns.nameServers`. If it does not exist, set `Programmed=False`, reason `ExternalResourceNotFound`. If it exists but TTL and values, or `AliasTarget`, differ, set `Programmed=False`, reason `ExternalResourceMismatch`.

Without adoption, if the same hosted zone ID, record name, and record type do not exist, the controller creates the record set. If an existing record set exists and `ZoneUnit.status.recordSets[].provider.data` already points to it, the same accepted owner item is considered to have previously programmed it and may update it. If an existing record set exists but status has no programmed identity, the controller does not adopt it automatically and sets `Programmed=False`, reason `ProviderConflict`.

Route 53 record sets cannot have tags. The initial implementation does not store record set ownership information provider-side. It does not use a TXT ownership registry. Ownership intent lives in `ZoneUnit.spec.recordSets[]`; observed result lives in `ZoneUnit.status.recordSets[]`.

Route 53-specific public record set information is stored in `ZoneUnit.status.recordSets[].provider.data` and projected to `RecordSet.status.provider.data`, validated by Route 53 Provider version `recordSet.schemas.statusProviderData`.

Route 53 `RecordSet.status.provider.data` fields:

- no fields are required in the initial schema.

Route 53 `Provider` schema descriptions for `recordSet.schemas.statusProviderData`:

| Field | `description` |
| --- | --- |
| _none_ | Route 53 record-set identity is derived from `ZoneUnit.spec.zone` and `ZoneUnit.spec.recordSets[]`. |

Route 53 record set batch changes are stored in `ZoneUnit.status.provider.state.pendingRecordSetChange`. Change IDs are not duplicated into claim status.

Example:

```yaml
status:
  observedGeneration: 1
  zone:
    ref:
      namespace: platform-dns
      name: apps-example-com
  provider:
    data: {}
  conditions:
    - type: Programmed
      status: "True"
      reason: Programmed
      observedGeneration: 1
```
