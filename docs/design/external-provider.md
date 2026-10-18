# External Provider

## Purpose and Scope

The external provider verifies customer-managed DNS. The customer owns authoritative zones, records, delegation, and TTLs. dns-api sends DNS queries and writes Kubernetes reconciliation status; it never creates, updates, adopts, or deletes DNS data. There is no DNS UPDATE, cloud DNS client, credential chain, or fallback DNS writer.

This document defines the stage-1 design, not evidence that the implementation or a deployment has passed verification. Stage 1 preserves the normal EndpointRecordSet → generated RecordSet → ZoneUnit → Accepted/Programmed path. The platform's Phase 4 acceptance is a remote-member HTTPPublish becoming Ready through real resolution and HTTPS, not merely creating a Provider or reporting a configured Zone. Platform wiring and that acceptance proof are outside this provider's implementation boundary.

The provider verifies retired-address exclusion through independent Core `RecordWithdrawal` claims and `ZoneUnit.spec/status.withdrawals[]`. This evidence is separate from ordinary Kubernetes deletion bookkeeping. Platform retirement and address reuse require their own current-result consumption and allocation/drain gates; this document does not establish deployment acceptance or global cache eradication.

## Provider Declaration

The Core Provider is `external.dns.appthrust.io/v1alpha1`: `Provider.metadata.name=external.dns.appthrust.io`, with one served and storage version named `v1alpha1`. The Provider object itself uses `apiVersion: dns.appthrust.io/v1alpha1`. The default controller name is `external.dns.appthrust.io/controller`.

Provider declaration fields:

| Field | Contract |
| --- | --- |
| `spec.display.name` | `External DNS` |
| `spec.display.description` | `Verifies customer-managed DNS without changing DNS records.` |
| `spec.versions[].identity` | Omitted. There is no ExternalIdentity resource. |
| `spec.versions[].zoneClass.schemas.parameters` | The parameter schema below. |
| `spec.versions[].zone.schemas.adoption` | Empty-object schema: `type: object`, `additionalProperties: false`, `properties: {}`. |
| `spec.versions[].recordSet.supportedTypes` | Exactly `A`, `AAAA`, and `CNAME`. |
| `spec.versions[].recordSet.schemas.options` and `.adoption` | The same empty-object schema. Omitted or empty payload is acceptable; nonempty payload is rejected. |
| `spec.versions[].zone.schemas.statusProviderData` and `.recordSet.schemas.statusProviderData` | Public verification data described below, with field descriptions and bounded lists. |
| `spec.versions[].zone.additionalConditionReasons` and `.recordSet.additionalConditionReasons` | The target-specific reasons in the condition tables below. |

External does not disable core record validations. Standard type/body exclusivity, required TTL, address-family checks, ownership arbitration, CNAME exclusivity, and apex CNAME rejection remain effective. A/AAAA describe the terminal address sets to verify, even when customer DNS uses a CNAME chain. CNAME describes an actual CNAME target to verify, not a provider alias.

Provider-specific Go types are inline payload/status types, including `ExternalZoneClassParameters`; there are no external Zone or RecordSet CRDs. The provider's sole storage version needs no Core provider-payload version conversion. That is separate from Endpoint App conversion below.

## ZoneClass Parameters

`ZoneClass.spec.identityRef` is omitted. Core admission requires a nonempty reference if and only if the selected Provider version declares `identity`, and forbids the field otherwise. Empty or sentinel identity objects do not represent credentialless operation. Route 53 and Cloudflare retain their required identity references.

`ZoneClass.spec.parameters` fields:

| Field | Description and bounds |
| --- | --- |
| `resolver.mode` | `Cluster` or `Recursive`. Omitted resolver or mode means `Cluster`. |
| `resolver.servers` | Recursive upstreams as canonical `IPv4:port` or `[IPv6]:port` strings. Required and nonempty for `Recursive`; omitted or empty for `Cluster`. At most 3 distinct endpoints. Port is a decimal integer from 1 through 65535 with no leading zeros. No hostname, URL, scoped IPv6 address, or implicit port. |
| `expectedNameservers` | Optional, at most 16 distinct canonical lowercase ASCII DNS names, without trailing root dots, IP literals, wildcards, or underscores. Omitted/empty means skip delegation and direct-authority checks. This is not a list of recursive resolvers. |

Use a separate ZoneClass for each domain whose expected authority differs. The platform projects its domain's `expectedNameservers` into this field; dns-api does not read PlatformDomain. Changing resolver policy invalidates prior probe success even if RecordSet generation is unchanged.

### Schema and CEL

The `zoneClass.schemas.parameters.openAPIV3Schema` fragment is:

```yaml
type: object
description: DNS resolver policy for verification without DNS writes.
additionalProperties: false
properties:
  resolver:
    type: object
    description: Resolver view used for every verification query.
    additionalProperties: false
    properties:
      mode:
        type: string
        description: Cluster uses pod DNS; Recursive uses every configured server.
        enum: [Cluster, Recursive]
      servers:
        type: array
        description: Explicit recursive DNS IP endpoints; never authoritative NS names.
        maxItems: 3
        uniqueItems: true
        items:
          type: string
          minLength: 3
          maxLength: 64
          pattern: '^(\[[0-9a-f:.]+\]|[0-9.]+):[1-9][0-9]{0,4}$'
  expectedNameservers:
    type: array
    description: Expected authoritative nameservers for the exact Zone domain.
    maxItems: 16
    uniqueItems: true
    items:
      type: string
      minLength: 1
      maxLength: 253
      pattern: '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$'
```

Omitted-field interpretation supplies Cluster; admission does not mutate the manifest. The following `zoneClass.validationRules` complement the schema in the existing KRO CEL environment (`self` is the ZoneClass). The schema first constrains endpoint separators and decimal ports; CEL checks real IP syntax, canonical addresses, port range, and mode/server exclusivity. No provider-specific webhook or new CEL dialect is introduced.

```yaml
- rule: >-
    has(self.spec.parameters.resolver) &&
    has(self.spec.parameters.resolver.mode) &&
    self.spec.parameters.resolver.mode == 'Recursive'
      ? (has(self.spec.parameters.resolver.servers) &&
         size(self.spec.parameters.resolver.servers) > 0)
      : (!has(self.spec.parameters.resolver) ||
         !has(self.spec.parameters.resolver.servers) ||
         size(self.spec.parameters.resolver.servers) == 0)
  message: resolver.servers must be nonempty for Recursive and empty for Cluster
- rule: >-
    !has(self.spec.parameters.resolver) ||
    !has(self.spec.parameters.resolver.servers) ||
    self.spec.parameters.resolver.servers.all(s,
      cel.bind(host, s.startsWith('[')
        ? s.split(']')[0].substring(1) : s.split(':')[0],
      cel.bind(port, s.startsWith('[')
        ? s.split(']:')[1] : s.split(':')[1],
        isIP(host) && ip.isCanonical(host) &&
        (s.startsWith('[') ? ip(host).family() == 6 : ip(host).family() == 4) &&
        int(port) >= 1 && int(port) <= 65535)))
  message: resolver.servers must contain canonical IP addresses and ports from 1 through 65535
- rule: >-
    !has(self.spec.parameters.expectedNameservers) ||
    self.spec.parameters.expectedNameservers.all(n, !isIP(n))
  message: expectedNameservers must contain DNS names, not IP addresses
```

Canonical server strings and names make `uniqueItems` a semantic duplicate check. DNS answers are normalized before comparison; manifests with noncanonical expectations are rejected rather than silently rewritten. Provider admission compiles these expressions as boolean rules and implementation tests exercise both omitted-field and malformed-input cases through the actual pinned CEL runtime.

Example:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: ZoneClass
metadata:
  namespace: platform
  name: customer-example
spec:
  provider:
    name: external.dns.appthrust.io
    version: v1alpha1
  controllerName: external.dns.appthrust.io/controller
  parameters:
    resolver:
      mode: Recursive
      servers:
        - 192.0.2.53:53
        - '[2001:db8::53]:53'
    expectedNameservers:
      - ns1.example.net
      - ns2.example.net
  allowedZones:
    namespaces:
      from: Same
```

These documentation addresses are not public resolver defaults.

### Controller Responsibility

The external ZoneClass controller matches controllerName and Provider binding, resolves the storage version, validates converted parameters, and sets `Accepted`. It makes no DNS queries and resolves no identity. Invalid configuration uses the existing `InvalidParameters`, `InvalidIdentityRef`, or `InvalidProvider` reasons; missing storage-version material follows the existing Unknown contract. A present `identityRef` on an external ZoneClass, or an absent one on a Route 53/Cloudflare class after a Provider change, is reported controller-side as `Accepted=False/InvalidIdentityRef`.

The external ZoneUnit controller watches ZoneUnit, matching ZoneClass, Provider, and namespace-policy changes. It reconciles only the configured provider/storage version through the matching class. Core remains the sole composer and public Zone/RecordSet status writer. The provider respects `Allowed`, `DeletionRequested`, claim UID, and claim observedGeneration; it does not probe denied ledger items or use old deletion receipts for a new incarnation.

The local read-only test/runtime boundary is `Resolver.Probe(ctx context.Context, request ProbeRequest) (ProbeResult, error)`, with injected clock and jitter. Requests contain checked names, expected RRsets, and effective resolver policy; results contain per-view RCODE, answers, AA, TTL/SOA, and authority evidence. There are no DNS Create/Update/Delete methods. The provider does not use a generic cloud provider interface or call a cloud API.

## Zone Semantics

An external Zone is an authorized logical suffix. It need not be a separately hosted or delegated zone. Without `expectedNameservers`, a customer may verify records under a suffix of an existing authority. No hosted-zone ID, adoption target, creation policy, or deletion policy is needed.

`Zone Accepted=True` confirms the binding and policy, not DNS reachability. `Zone Programmed=True` means the verifier is configured and, if requested, the exact Zone's nameserver constraint has passed. It is independent of record success: a missing application record must not make the Zone unconfigured or create a Zone ↔ RecordSet readiness cycle. Conversely, failed or unknown required nameserver checks prevent child RecordSets becoming Programmed=True, even if their recursive answers match.

With `expectedNameservers`, the exact `Zone.spec.domainName` must be a zone apex in the selected view. A parent authority's NS set cannot be relabelled as a successful delegation check for a nondelegated suffix. `Zone.status.nameServers` contains only a completed observed NS set, not copied expected values; omit it if no set was observed. Conditions and probe times distinguish last observation from current success.

## RecordSet Verification

For every active allowed name, collect its A and AAAA desired ledger items together. Normalize addresses to their IP values, normalize DNS answer names to lowercase without the root dot, deduplicate, and sort for deterministic diagnostics. Probe **both** address families. The observed A set must equal the expected A set, and the observed AAAA set must equal the expected AAAA set in every required view. A missing desired family means an empty expected set, not permission to ignore that family's answers. NODATA for an empty expected family is acceptable; NXDOMAIN for a name with expected data is not.

Set equality is exact: missing, unexpected, or additional-family addresses prevent success. Do not accept subsets, any matching VIP, an address in an allocated pool, or a union assembled from disagreeing resolvers. The two address-family claims share the combined result, so an A-only success cannot hide an unexpected AAAA answer. Adding/removing a sibling family changes the combined expectation and invalidates the surviving claim's earlier receipt.

For IP intents, a customer CNAME chain is allowed only when terminal address sets satisfy the same predicate. Follow at most 8 CNAME links total, detect repeated canonical names, and reject loops. A chain limit or loop is not an address match. Preserve relevant chain TTLs for scheduling. A CNAME to a dual-stack target for an IPv4-only intent is a mismatch by design; the customer must point at an address set equal to the published one.

For CNAME intent, the first canonical CNAME target at the checked owner must equal `spec.cname.target`. Another name with the same terminal addresses is not equivalent. Follow the expected target within the same 8-link/loop bounds and require usable terminal A or AAAA resolution in every required recursive view. Both families are inspected; address equality is not asserted against an IP intent that does not exist. Required views must agree on the observed chain and terminal sets. A CNAME returned together with incompatible same-owner data is not successful verification. CNAME at `name: "@"` remains rejected by core.

### Wildcard Witness

A wildcard RecordSet is verified by a concrete witness, not a query for the literal `*`. Replace the leftmost wildcard label with a stable DNS-safe label derived from the Zone identity and the sorted active claims' UID/generation tuple. All A/AAAA items for that wildcard use the same witness, including when they were created in different orders. Repeated timer checks of unchanged intent reuse it; recreation or changed desired intent changes it.

Expose `checkedName` and `sampledWildcard=true` in public data. This proves only the sampled name. Existing explicit owners or closer enclosers can override wildcard synthesis, so every exact application hostname is also independently probed. It does not prove all descendant names or TLS wildcard coverage.

For the platform's dedicated-member topology, customer records are `B` and `*.B` for the PMC public edge and `*.<Project>.B` for that Project's actual serving Gateway addresses. A wildcard pointing at the union of reserved member VIPs cannot encode Project placement and must fail when it differs from the intended targets. A single wildcard is adequate only when all covered names deliberately share the same serving address set. Cross-namespace wildcard claims require an explicit `allowedRecordSets.records` grant on the Zone ([Core API](core-api.md#allowedrecordsets)); the suffix match alone does not authorize them.

## Resolver Policy

| Policy | Required observation | Scope and failure behavior |
| --- | --- | --- |
| `Cluster` (default) | Read pod `/etc/resolv.conf` nameservers and query each independently using DNS wire queries with RD=1. Use absolute FQDNs, without search-path expansion. Apply the same maximum of 3 distinct upstreams; empty, invalid, or oversized configuration fails closed. | Proves only the controller/PMC resolver view. Do not treat resolv.conf order as failover or silently discard an upstream. |
| `Recursive` | Query every configured IP:port endpoint independently with RD=1; all must match. | Supports operator-selected private/split-horizon or public views. No fallback to cluster DNS or hardcoded public resolvers after failure. |
| Nonempty `expectedNameservers` | Additionally query the exact Zone apex NS and SOA through every selected recursive view, compare the normalized NS set exactly, and require the SOA owner to identify that apex. Resolve every expected NS host through those views, then query every discovered authority address with RD=0. Require AA and the expected apex NS/SOA and in-zone record data. | Every required authority must agree with expected data; a failed authority is not ignored. At most 2 distinct addresses per expected NS after deduplication across views; an excess is an explicit probe-limit failure, not successful sampling. When a CNAME leaves the asserted zone, verify its in-zone authoritative link and follow the remaining chain through the chosen recursive view, not by demanding that the original authority serve the foreign zone. |
| Empty `expectedNameservers` | Recursive view only. | No delegation or direct-authority verification claim. |

Direct-authority checks also fail closed for an in-suffix delegation whose data the expected authority does not authoritatively serve; the provider does not infer and trust an additional child authority. Configure a separate Zone/Class for a separately delegated child.

Use a DNS wire client exposing RCODE, AA, TTL, SOA, and TC (for example `miekg/dns`), rather than `net.Resolver.LookupIPAddr` alone. Send QUERY only, UDP first and TCP on truncation. Resolve NS host addresses through configured resolvers, not an implicit OS fallback. No DNSSEC validation, root-to-parent iterative delegation proof, or public/global reachability guarantee is claimed: the recursive view and network are trusted inputs.

Use the minimum relevant positive TTL across the completed chain; negative cache TTL is `min(SOA TTL, SOA.MINIMUM)` for NXDOMAIN/NODATA when an SOA is present. Expose observed TTLs/negative expiry for diagnostics. A matching authority plus stale recursive NXDOMAIN remains unverified until the recursive view also matches. Do not flush shared caches, change customer TTLs, install an additional process-wide answer cache, or interpret a polling cap as cache expiry.

## Conditions and Public Data

A valid claim keeps `Accepted=True` while DNS is wrong or unavailable. Only `Programmed` reflects probe observations. Each receipt uses the exact claim UID and observedGeneration. No provider-owned `Published` or Endpoint `Ready` condition is added; the existing endpoint controller aggregates nonempty generated RecordSets with current Accepted/Programmed evidence.

RecordSet `Programmed` reason and stable message templates:

| Status | Reason | Message |
| --- | --- | --- |
| True | `ResolutionVerified` | `DNS <fqdn> matches expected targets in every required resolver view; no DNS changes were made.` |
| False | `DNSNameNotFound` | `DNS <fqdn> returned NXDOMAIN from <view>; expected <targets>.` |
| False | `DNSNoData` | `DNS <fqdn> returned NOERROR with no <qtype> answers; expected <targets>.` |
| False | `ResolutionPartial` | `DNS <fqdn> is missing expected targets <missing>; observed <actual>.` |
| False | `ResolutionMismatch` | `DNS <fqdn> targets differ: missing <missing>; unexpected <extra>.` |
| False | `ResolutionInconsistent` | `DNS <fqdn> differs between required resolver/authority views <views>.` |
| False | `NameserverMismatch` | `DNS <zone> nameservers differ: expected <expected>; observed <actual>.` |
| Unknown | `DNSProbeFailed` | `DNS verification unavailable for <fqdn> from <view>: <failure>.` |

`ResolutionPartial` means a nonempty proper subset with no unexpected values. Empty required-type answers use DNSNoData; any unexpected value uses ResolutionMismatch. `<targets>` includes typed values (A, AAAA, or CNAME). CNAME loops, incompatible data, and a wrong first target are ResolutionMismatch, with the observed chain included in bounded public data. `<failure>` is one of `timeout`, `SERVFAIL`, `REFUSED`, `transport`, `invalid-response`, `resolver-config`, `chain-limit`, or `probe-limit`; raw errors/packets are not embedded in messages.

A required incomplete/failed probe is Unknown/DNSProbeFailed, never last-known success. Transient `Unknown/DNSProbeFailed` immediately drops EndpointRecordSet `Ready` through the existing generated-RecordSet aggregation; there is no grace window. Among completed observations, differing required views take precedence as ResolutionInconsistent; a consistently wrong expected NS set is NameserverMismatch; otherwise use the negative/partial/mismatch result. Normalize recursive and authoritative observations to the checked owner and terminal data before comparing, so normal AA/TTL differences are not disagreements. All-empty-family NODATA is not an inconsistency. Select the first failing view in canonical order for single-view message templates.

Zone `Programmed` reasons:

| Status | Reason | Message |
| --- | --- | --- |
| True | `VerificationConfigured` | `DNS verifier configured for <zone>; authoritative nameserver checks were not requested.` |
| True | `NameserversVerified` | `DNS <zone> nameservers match expectations in every required view; no DNS changes were made.` |
| False | `NameserverMismatch` | The nameserver mismatch template above; includes a non-apex NS expectation. |
| False | `ResolutionInconsistent` | The inconsistent-view template with the Zone domain as `<fqdn>`. |
| Unknown | `DNSProbeFailed` | The probe-failure template with the Zone domain as `<fqdn>`. |

Register exactly these extra status/reason combinations in the Provider's respective Zone and RecordSet `additionalConditionReasons`. Propagate a required Zone authority failure/unknown to affected RecordSets. Before any completed observation, use the existing Unknown/Reconciling condition. Unit-level Programmed uses only the fixed allowlist (`validator.go` ZoneUnit status): True/`Programmed` when every active observation succeeds; False/`ExternalResourceNotFound` when the first failing target is `DNSNameNotFound`; False/`ExternalResourceMismatch` for every other completed mismatch (`DNSNoData`, `ResolutionPartial`, `ResolutionMismatch`, `ResolutionInconsistent`, `NameserverMismatch`); Unknown/`ProviderUnavailable` for `DNSProbeFailed`. The provider implements the same precedence as the Route 53 unit aggregate (`zone_status.go`): any False target wins; Unknown only when no target is False. Target reasons are mapped, never copied, into unit-level status.

Core projection preserves the selected current provider condition's `Message` along with Status/Reason for both Zone and RecordSet, including an empty message; it must not replace diagnostics with the reason string. Core-generated waiting/rejection conditions retain their own messages. Existing UID/generation guards and acceptance precedence remain in force.

`Zone.status.provider.data` and `RecordSet.status.provider.data` expose:

- `resolverMode`, `checkedName`, and, for RecordSets, `sampledWildcard`.
- Sorted typed `actual`, `missing`, and `extra` values and bounded CNAME-chain/view diagnostics. Each list is capped at 16 entries with total counts and an explicit truncation flag; equality is evaluated on complete observations, never truncated status.
- `observedTTLSeconds`, `negativeTTLSeconds`, and `negativeExpiryTime` when the corresponding evidence exists; no invented zero TTL for missing SOA.
- `lastProbeTime` (last completed attempt, successful or failed) and `nextProbeTime` (approximate next planned check).

Messages are capped at 2048 bytes, use deterministic sorted list previews with omitted counts, and contain no credentials or raw packets. Successful timestamps/next-probe updates do not emit repeated success Events or change LastTransitionTime when condition status is unchanged. Events report transitions only; status remains the source of truth.

Private `ZoneUnit.status.provider.state` holds advisory due times, fair-resumption cursor, backoff, and the resolver-policy/desired-name fingerprints. A probe receipt is bound to the claim tuple, all sibling-family desired inputs, the Zone binding, and effective normalized Class/Provider/resolver configuration. Before committing an asynchronous result, recheck that those inputs still match; otherwise discard it and queue current intent. Class/Provider policy changes and resolv.conf changes invalidate old success. Restart reconstructs scheduling from current claims and does not trust obsolete private state.

## Scheduling and Resource Bounds

DNS edits and recursive cache expiry have no Kubernetes watch. A timer is unavoidable: a customer may fix DNS without changing any Kubernetes object. Probe immediately on new/changed intent or effective policy and keep rechecking successful claims to detect drift. Kubernetes watches complement rather than replace timers. A later failed probe removes Programmed=True.

| Outcome | Base next-check interval |
| --- | --- |
| Pending, partial, or mismatch | 30 seconds |
| Success | Minimum relevant positive TTL, clamped to 30–300 seconds |
| NXDOMAIN/NODATA | Negative TTL, clamped to 30–300 seconds |
| Missing relevant TTL | 30 seconds |
| Unavailable probe | Backoff 30 → 60 → 120 → 300 seconds; reset after a completed non-error observation or changed inputs |

Apply ±10% injected jitter, then clamp again to 30–300 seconds. These are polling bounds, not an external propagation deadline or a wall-clock freshness promise. Customer caches can outlive the polling cap. A stopped controller cannot withdraw a previously published condition; expose probe times and monitor controller health. Consumers that need freshness treat a claim as stale when `now > nextProbeTime + 300 s`; conditions alone do not encode this. Existing EndpointRecordSet aggregation reads conditions, not that timestamp threshold; it immediately drops Ready on an observed Unknown/DNSProbeFailed, without a grace window.

Every exchange has a 2-second timeout. Each name has a 10-second deadline that starts when its first exchange is admitted by the limiter; limiter wait time is not charged to the name. Zone-apex NS/SOA queries and expected-NS host resolution are performed once per Zone per probe cycle and shared by all names in that cycle, keyed by the resolver-policy fingerprint; only in-zone A/AAAA/CNAME queries are per name. The manager-wide limiter is sized so that 4 concurrent names with the maximum authority fan-out (16 NS × 2 addresses × 2 qtypes) complete inside the name deadline (default 40 QPS, burst 80), and is a manager flag. Exhausted deadlines or limits are Unknown, never success from a partial sample.

The manager admits at most 4 concurrent names. The limiter flags are `--external-dns-qps` (default `40`) and `--external-dns-burst` (default `80`); they apply across all ZoneUnits. The per-cycle Zone evidence is not a process-wide answer cache and is invalidated by a resolver-policy change. Limiter queue delay remains observable separately from admitted probe duration.

Process at most 32 due names per ZoneUnit reconcile. Resume fairly through private state and use a 1-second continuation while previously unprocessed due work remains; this exception schedules other due names, not another probe of a completed name before its 30-second floor. Recompute the next scheduled reconcile from the earliest due time when the backlog is drained. Queue saturation may exceed the nominal cadence and must be visible in queue-delay metrics. The provider's own unchanged status events must not trigger fresh probes; keep the core watch that projects genuine status updates. Patch status/receipts, not claim spec or arbitrary timestamps on every watch event.

## Deletion

External owns no DNS object. For a matching-UID ledger item with `DeletionRequested=true`, stop probes and mark `DeletionCompleted=true` after Kubernetes bookkeeping. Complete ZoneUnit finalization without a DNS delete call and without waiting for the customer to remove a wildcard. Existing core claim/endpoint finalizers still wait for the appropriate Kubernetes receipts and generated-child disappearance. Old-incarnation deletion completion must not finalize a replacement claim.

**Deletion completion is not verified DNS withdrawal.** Claim disappearance, observedGeneration, a completed verifier finalizer, and timer expiry do not prove address exclusion. Independent retained `RecordWithdrawal` claims continue verification after generated RecordSets and EndpointRecordSets disappear. Pending withdrawals do not affect unrelated active Zone/RecordSet `Programmed` aggregation. Every retained withdrawal, including a verified one, blocks Zone/ZoneUnit deletion until explicitly archived or removed.

### Withdrawal Verification

Withdrawal uses a separate exclusion evaluator, not the active exact-set evaluator:

```text
excluded(address) := address ∉ normalize(terminal A ∪ terminal AAAA)
```

Every checked name is probed in both families through every effective Cluster/Recursive resolver and, with `expectedNameservers`, every discovered authority endpoint via every configured resolver. Required NS/SOA/delegation and authoritative-answer checks still apply. Replacement addresses may remain and differ between views. Complete NOERROR replacement answers prove current exclusion; NODATA/NXDOMAIN require valid negative SOA evidence. Missing views/families, failures, malformed or unresolved chains, loops, and CNAME/probe limits are Unknown, never partial success. A foreign CNAME boundary is not sufficient exclusion evidence: complete terminal A/AAAA evidence is required.

A wildcard uses a stable concrete `dns-retire-` witness derived from the claim UID, Zone reference, and Unit UID, plus any retained publication witness for that address. Recreated claims use a different witness. `sampledWildcard=true` describes sampling, not exhaustive proof for descendants or explicit owners.

The first complete all-view exclusion starts `absentSince=t0`. `notBefore` is the maximum of t0, retained positive-address/CNAME expiry bounds, and `t0 + negativeTTL` for every required negative observation; negative TTL is `min(SOA TTL, SOA.MINIMUM)`. Complete per-view/per-family evidence drives this calculation, not bounded diagnostic previews or minimum-TTL summaries. Repeated identical negatives do not slide t0; larger bounds extend but never shorten the window. Public horizons round up to a whole second. A separate complete probe at or after `notBefore`, later than the preceding attempt, is required even for zero TTL. Polling caps never shorten a cache horizon.

Reappearance, failed/incomplete observations, changed policy/views, clock reversal, or a schedule stale beyond `nextProbeTime + 300s` invalidates success and the absence window while retaining known positive horizons. Restart preserves only matching persisted identity/policy/window evidence. Required resolver, expected-nameserver, and discovered-view history remains a floor: weakening or missing historical evidence reports `PolicyChanged` rather than proving equivalence. Retained claims are reverified; consumers also reject stale/future/missing timestamps and mismatched bindings, and require `verifiedAt >= notBefore`.

`Accepted=True/Accepted` means valid authorized intent; rejection uses `InvalidWithdrawal` or `NotAllowed`. Core also reports `UnsupportedProvider`. Provider `Withdrawn` and `Ready` use:

| Withdrawn | Ready | Reason |
| --- | --- | --- |
| False | False | `DNSStillResolves` |
| False | False | `NegativeTTLNotElapsed` |
| Unknown | False | `DNSProbeFailed` |
| Unknown | False | `PolicyChanged` |
| True | True | `WithdrawalVerified` |

Messages retain these templates, with bounded 2048-byte diagnostics:

- `DNS <checkedName> still resolves to retired address <address> in <view> (<qtype>).`
- `Retired address <address> is absent in every required view; waiting until <notBefore> for cache safety and a confirming probe.`
- `Retired address <address> was verified absent from <name> in every required view at <verifiedAt>.`
- `External DNS withdrawal cannot be verified for <name> in <view>: <closedFailureCode>.`
- `External DNS withdrawal policy changed for <name>; the prior required view policy remains a floor.`

Core supplies a validated waiver's exact name, UID, immutable-spec digest, and receiving reservation namespace/name/UID. The provider echoes that scope and emits `OverrideAccepted=True/OperatorAuthorizedRelease` with message `Core-validated withdrawal waiver is bound to its exact UID and immutable scope.` This never changes `Withdrawn` or `Ready` into DNS success and performs no platform-object lookup.

Withdrawal work shares the existing limiter, concurrency, transport, deadlines, jitter, and retry intervals. A separate persisted fair cursor schedules claims; each concrete checked name consumes one name-budget slot. When active and withdrawal work are both due, withdrawal selection receives half the pass budget and active work uses the remaining budget. Requeue uses the earlier active/withdrawal deadline (at least one second); a deleting Unit still verifies retained withdrawals. There is no timeout-based release. `dns_external_withdrawals_pending{reason}` counts nonverified claims with the closed labels `DNSStillResolves`, `NegativeTTLNotElapsed`, `DNSProbeFailed`, `PolicyChanged`, `InvalidWithdrawal`, `NotAllowed`, and `UnsupportedProvider`; other reasons fold into `DNSProbeFailed`. Existing probe, duration, and queue-delay metrics remain shared.

## Admission and Security

Admission performs no network/DNS queries. Reuse Provider OpenAPI/CEL and core webhook rules for shape, immutable references, supported types/bodies, ordinary TTL range, and object-local namespace-policy declarations. Cross-object grants and ownership remain core composition checks. Reject external identityRef and nonempty options/adoption, including Route 53 alias and Cloudflare proxy fields. Keep all ordinary CNAME/record ownership protections.

Only operators may create/update Providers and resolver-bearing ZoneClasses. Tenants cannot choose arbitrary DNS destinations by creating a RecordSet. Explicit private/RFC1918 resolvers are allowed for on-premises use; DNS egress controls must allow the intended recursive and optional authority endpoints, not blanket-ban private IPs or silently change the view. Resolver-address errors fail closed.

External requires no Secret get/list/watch, identity-resource access, IAM/STS calls, token access, or credential environment configuration. Kubernetes API authentication and mounted webhook TLS serving certificates are not customer DNS credentials; they do not require the controller to read the Kubernetes Secret API. An external-only chart deployment must remove cloud provider RBAC and Secret informers, not just omit cloud Provider manifests. Existing core/endpoint resources, namespace reads, status/finalizers, Events, leader-election leases, and the enabled conversion API's create permission remain.

## Endpoint Conversion Decision

**Decided for stage 1: retain the provider-specific aggregated Endpoint RecordSet Conversion API; reject schema-equality-inferred built-in identity conversion.** No Core Provider or EndpointProviderCapability API field is added for an inferred bypass.

Grounding at source revision `87aa1dc`:

- `internal/go/apps/endpoint/controllers/endpointrecordset/controller.go:298-374` always creates the capability's conversion GVR (apart from an injected test function), requires a REST config, checks response UID/result/nonempty fragments, and rejects changed names or unsupported output types.
- `pkg/go/api/endpoint/v1alpha1/endpointprovider_capability_types.go:21-41` requires `spec.conversion` with group/resource and an optional version. There is no built-in or identity mode.
- `internal/go/providers/route53/conversion/handler.go:81-186` classifies targets, splits IPv4/IPv6, chooses TTL, rejects unsupported mixtures, and maps a hostname to aliases unless CNAME is explicitly requested. Its standard bodies do not establish identical conversion policy.
- Core Provider storage conversion operates on `{options, adoption}` (and other provider-owned payloads), not a full endpoint target list or RecordSetSpecFragment. Same storage version already avoids Core payload conversion, but does not transform endpoint intent.

Schema equality therefore cannot prove semantic identity: input targets and output fragments have different shapes/cardinalities, and optional alias policy can differ despite shared standard record bodies. A future in-process standard-record converter is technically possible, but would need an explicit Endpoint App capability discriminator and a versioned shaping contract; it belongs to the App, not Core. It cannot be safely inferred from missing identity, A/AAAA/CNAME support, or schema similarity. That additional API is not needed for stage 1. The chosen tradeoff retains APIService/TLS operational cost and the existing discovery/authentication/RBAC/UID validation path instead of creating a second implicit convention.

The exact capability uses the existing API unchanged:

```yaml
apiVersion: endpoint.dns.appthrust.io/v1alpha1
kind: EndpointProviderCapability
metadata:
  name: external-v1alpha1
spec:
  provider:
    name: external.dns.appthrust.io
    version: v1alpha1
  conversion:
    group: endpoint.external.dns.appthrust.io
    version: v1alpha1
    resource: endpointrecordsetconversions
```

The matching APIService is `v1alpha1.endpoint.external.dns.appthrust.io`. A stateless handler on the existing manager webhook server serves discovery and POST-only resource creation at `/apis/endpoint.external.dns.appthrust.io/v1alpha1/endpointrecordsetconversions`; it does not persist conversion objects. Reuse the existing request/result/UID framing, never the Route 53 alias behavior. Conversion performs no DNS queries and has no credentials.

For IP-only targets, emit nonempty A and/or AAAA fragments in that order after canonicalization, deduplication, and sorting. For exactly one distinct hostname target, emit CNAME; reject apex CNAME, mixed IP/hostname targets, and multiple distinct hostnames. Preserve `input.name`, including wildcard form. An absent preferredRecordType or CNAME preference is permitted for hostname targets; a preference incompatible with target kinds is rejected. Every emitted standard fragment has `ttl: 300`, no options/adoption, and its matching core body, including CNAME, unlike the Route 53 handler's 60-second CNAME TTL. The TTL satisfies the existing schema; external does not set customer TTL or require observed TTL equality. The endpoint controller currently derives `preferredRecordType` only from the `endpoint.dns.appthrust.io/route53-record-type` annotation; external accepts the forwarded value but does not introduce a second annotation. Renaming that annotation is an Endpoint App change outside this document.

Conversion failures use `InvalidInput` for malformed input and `UnsupportedTarget` for unsupported target/preference combinations, with `retryable=false`; success uses `Converted`. Mixed targets use `External endpoint conversion does not support mixed hostname and IP targets`; multiple hostname targets use `External endpoint conversion requires exactly one hostname target`; apex uses `External endpoint conversion does not support CNAME at the zone apex`. UID, nonempty-fragment, name, and type checks remain mandatory in the endpoint controller. Failure never produces an empty ready endpoint.

## Packaging and Metrics

`providers.external.enabled` defaults to false. `--external-controller-enabled` gates external ZoneClass/ZoneUnit registration and its conversion handler. Provider/controller-name/version flags and environment variables follow existing provider bindings: `--external-controller-name` / `EXTERNAL_CONTROLLER_NAME`, `--external-provider-name` / `EXTERNAL_PROVIDER_NAME`, and `--external-provider-version` / `EXTERNAL_PROVIDER_VERSION`.

Provider values must actually gate registrations and provider-only privileges as specified in [Controllers and Webhooks](controllers-webhooks.md#provider-enablement-required-contract-not-implemented-at-87aa1dc). Enabling external installs its Provider, capability, APIService, conversion-create RBAC, and handler together. Do not render an external capability that cannot be served. External enablement requires webhook TLS with a valid CA bundle and certificate; the chart `fail`s rendering when `providers.external.enabled=true` and either `webhook.enabled=false` or `certManager.enabled=false`; a user-supplied `webhook.caBundle` is not provided in this revision. Do not copy an `insecureSkipTLSVerify` fallback. No external conversion-disable sub-option is needed. Core admission remains available regardless of which providers are enabled.

Synchronize generated API types/deepcopy, both CRD distributions, Go identity-using callers, `docs/design/ui.md`, and `pkg/ts/ui/src/views/platform/ZoneClassForm.tsx` when making identityRef optional. The UI supports external ZoneClass creation in stage 1: it selects the Provider without an identity, omits `identityRef` and the identity picker, and exposes external resolver parameters. A new chart with an old ZoneClass CRD still rejects credentialless classes; separate CRD carriers must be upgraded too. Helm and Kustomize admission wiring must agree, including ZoneClass status and enabled identity webhooks. Existing Gateway/Service watcher switches and the single controller ServiceAccount binding remain independent.

Use the existing manager metrics registry/server:

| Metric | Type and labels |
| --- | --- |
| `dns_external_probes_total{outcome,qtype}` | Counter; qtype is A, AAAA, CNAME, NS, or SOA; outcome is verified, mismatch, negative, or failed. |
| `dns_external_probe_duration_seconds{outcome}` | Histogram of completed probe durations. |
| `dns_external_pending_records{reason}` | Gauge of active unverified record claims, using only the closed reason set above plus Reconciling. |
| `dns_external_probe_queue_delay_seconds` | Histogram of delay from due time to attempt start. |

No hostname, namespace, IP, UID, or resolver-address labels. Do not expose packet/credential content in logs or metrics.

## Tests

Follow [Test Policy](test-policy.md): exhaustive deterministic unit/envtest coverage, representative black-box success paths only for kest/conformance. The external success path is `tests/e2e/external/external-a-record-lifecycle.test.ts`, using an in-kind authoritative CoreDNS `file` fixture whose zone the harness edits to model the customer; it asserts mismatch→match→drift without public DNS. No public DNS, cloud credentials, or real customer resources are used in external-provider CI. Controlled local DNS fixtures may be changed by the test harness to model the customer; the provider itself must remain read-only.

### Unit and Local Transport

Inject a fake Resolver, clock, and jitter. Cover exact sets, order/duplicates, canonical IPv4/IPv6, unexpected opposite family, independent per-host outcomes, CNAME first-target identity and terminal resolution, eight-link boundary/loops, shared wildcard witness and explicit-owner overrides. Cover NXDOMAIN, empty expected-family NODATA, partial/mismatch, disagreeing views/authorities, nondelegated suffix, missing authoritative AA, foreign CNAME targets, and every failure mapping.

Use fake time for positive/negative SOA TTLs, missing TTL, clamping/jitter, 30/60/120/300 backoff, policy-change reset, mismatch→match without Kubernetes writes, and drift after success. Cover rate/concurrency limits, limiter-admission deadline start, per-Zone NS/SOA and NS-host work sharing/invalidation, finite per-pass work, fair continuation, observer status events not becoming probe loops, and no success from incomplete fan-out. Verify UID/generation/sibling/policy fingerprints, same-name recreation, stale results, Allowed=false, and incarnation-matched deletion completion.

Run transport tests against a loopback UDP/TCP fixture for RCODE/AA/SOA parsing, truncation fallback, malformed replies, and cancellation. Assert only QUERY packets, never UPDATE, and no implicit resolver fallback or cloud client calls. Test conversion HTTP framing, discovery, UID, typed fragments, unsupported targets/preferences, and TTL compatibility without DNS access.

### Envtest

Install the real regenerated CRDs and Provider manifest, enable core admission and external Class/Unit and endpoint controllers, and inject a controlled resolver. Create Class → Zone → EndpointRecordSet and assert generated RecordSets and exact current conditions through mismatch → match → drift without spec updates. A configured Zone must remain independent of child failures; failed nameserver checks must block otherwise-matching children. Assert diagnostic Message projection for both Zone and RecordSet, zero/stale-child readiness rejection, immediate Endpoint Ready loss on Unknown/DNSProbeFailed without a grace window, cross-namespace wildcard grants, finalizers, mapped unit-level reasons, and deletion bookkeeping without DNS mutation.

Exercise external identityRef rejection, Route 53/Cloudflare missing-identity rejection, controller-side InvalidIdentityRef after Provider identity changes, canonical parameter/CEL validation, invalid status reasons, storage-version handling, and old-schema incompatibility. Tests must use real CRD/admission paths, not just direct helpers. Separately configure envtest aggregation, request-header authentication, serving TLS/CA, and RBAC to exercise the real APIService conversion path including denied/unavailable/malformed cases. An injected conversion function does not prove that wiring.

### Chart and Registration Contracts

Cover external-only, each cloud-provider enable combination, all-disabled, and independent Gateway/Service source switches. Assert flags, actual controller/identity-webhook/conversion registrations, rendered Provider/capability/APIService agreement, valid CA wiring, rendering rejection when webhook or cert-manager is disabled for external, and Helm/Kustomize admission parity. External-only must have no cloud identity or Secret RBAC/informers; disabled source controllers must not restore their unrelated permissions. Retain the single intended controller ServiceAccount binding and CRD-copy synchronization contracts.

Platform acceptance uses an explicitly authorized run with real customer-controlled DNS and remote-member HTTPPublish Ready, including wrong-target/wrong-NS recovery. Forced-IP HTTP, TLS verification bypass, borrowed synthesis domains, public DNS in CI, and unexecuted test plans are not verification evidence.
