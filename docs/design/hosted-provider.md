# Hosted Provider

## Purpose and Scope

The hosted provider makes dns-api the authority for a zone. It is the first Core Provider that both writes records and answers DNS queries for them, without any cloud DNS API, credential, or customer-managed zone. The customer delegates a sub-zone once (NS plus glue in their parent zone); every later record change is served by dns-api itself.

The provider is two runtime components that share one Kubernetes contract and nothing else:

- The **hosted controller** is an ordinary Core Provider controller. It reads `ZoneUnit.spec`, decides the complete record content of each zone, writes it to a `HostedZone` serving snapshot, and reports `ZoneUnit.status`. It never speaks DNS on the wire except through the read-only probe used for withdrawal verification.
- The **hosted server** is an authoritative DNS server. It watches `HostedZone` snapshots, serves them over UDP and TCP, and reports the serial it serves on `HostedZone.status`. It never reads `Zone`, `RecordSet`, or `ZoneUnit` and never writes anything but its own status entry.

Splitting decision from serving keeps the serving path free of composition logic and lets the server scale and restart independently. The `HostedZone` snapshot is the only coupling: the controller is its single spec writer; servers are its readers and the sole writers of their own status entries.

This document defines the design, not evidence of a verified deployment. Platform wiring, VIP allocation, and the delegation acceptance proof are outside this provider's boundary.

## Provider Declaration

The Core Provider is `hosted.dns.appthrust.io/v1alpha1`: `Provider.metadata.name=hosted.dns.appthrust.io`, one served and storage version `v1alpha1`, default controller name `hosted.dns.appthrust.io/controller`. Runtime binding follows [provider-extension.md](provider-extension.md#provider): `--hosted-controller-name` / `HOSTED_CONTROLLER_NAME`, `--hosted-provider-name` / `HOSTED_PROVIDER_NAME`, `--hosted-provider-version` / `HOSTED_PROVIDER_VERSION`, and the enable switch `--hosted-controller-enabled` / `HOSTED_CONTROLLER_ENABLED` (default `false`) gated with manager registration, conversion handler, admission, and RBAC as [Provider Enablement](controllers-webhooks.md#provider-enablement) requires.

| Field | Contract |
| --- | --- |
| `spec.display.name` | `Hosted DNS` |
| `spec.display.description` | `Serves zones authoritatively from Kubernetes; no external DNS provider.` |
| `spec.versions[].identity` | Omitted. Credentialless. |
| `spec.versions[].zoneClass.schemas.parameters` | The parameter schema below. |
| `spec.versions[].zone.schemas.adoption` | Empty-object schema. |
| `spec.versions[].recordSet.supportedTypes` | `A`, `AAAA`, `CNAME`, `TXT`, `MX`, `CAA`, `NS`. |
| `spec.versions[].recordSet.schemas.options` / `.adoption` | Empty-object schema. |
| `spec.versions[].recordWithdrawal` | `supported: true` with the status schema and the eleven closed reason combinations defined for [external withdrawal](external-provider.md#withdrawal-verification). |
| `spec.versions[].zone.additionalConditionReasons` | `ServingPending`, `SnapshotRejected`. |
| `spec.versions[].recordSet.additionalConditionReasons` | `ServingPending`. |

Hosted keeps every core record validation. Apex `NS` claims are rejected by a ZoneUnit validation rule: the apex NS RRset is owned by the zone's `nameServers` parameter, never by a `RecordSet`.

## ZoneClass Parameters

`ZoneClass.spec.identityRef` is omitted.

| Field | Description and bounds |
| --- | --- |
| `nameServers[]` | Required, 1–8 items. Each has `name` (canonical lowercase FQDN without trailing dot) and `addresses[]` (1–8 canonical IPv4/IPv6 literals). These become the apex NS RRset and, for in-zone names, the glue A/AAAA RRsets of every zone created from the class. Addresses are required even for out-of-zone names so the platform can publish deterministic glue. |
| `soa.rname` | Mailbox name, default `hostmaster.<domainName>`. |
| `soa.refresh`, `soa.retry`, `soa.expire`, `soa.minimum` | Seconds; defaults 3600, 900, 1209600, 300. `minimum` is the negative-caching TTL the withdrawal evaluator uses. 1–2419200. |
| `defaultTTL` | Seconds, default 60, 1–86400. Applied to NS, glue, and endpoint-converted record sets. |
| `verification.views[]` | Optional, at most 3 canonical `IP:port` endpoints the withdrawal evaluator queries. Omitted means the in-cluster hosted server Service (`<service>.<namespace>.svc:53`). The platform sets its public VIP here so the withdrawal proof is taken from the address clients use. |
| `verification.delegation.resolvers[]` | Optional, at most 3 recursive `IP:port` endpoints. When set, the controller probes `NS <domainName>` through them and reports the observed parent delegation in `zone.provider.data.delegation`. Observation only; it never changes `Programmed`. |

SOA `mname` is `nameServers[0].name`. SOA serial is controller-owned (below). Changing `nameServers` or `soa` re-renders every zone of the class.

## HostedZone Snapshot

`HostedZone` (`hosted.dns.appthrust.io/v1alpha1`) is namespace-scoped and uses the same namespace and name as the `ZoneUnit`. The hosted controller creates, updates, and deletes it; it carries an ownerReference to the `ZoneUnit`.

`spec`:

- `domainName`: canonical zone apex.
- `serial`: uint32, strictly increasing per snapshot. The controller sets `max(previous+1, unixSeconds(now) mod 2^32)` on every content change and never decreases it.
- `soa`: rendered `mname`, `rname`, `refresh`, `retry`, `expire`, `minimum`.
- `nameServers[]`: apex NS names with glue addresses, from the class.
- `records[]`: complete rendered RRsets, map-list keyed by `name` (zone-relative, `@` for apex, leftmost `*` allowed) and `type`. Each has `ttl` and `values[]` (canonical presentation strings: addresses, lowercase FQDNs with trailing dot, quoted TXT chunks, `pref target` for MX, `flags tag "value"` for CAA). At most 10000 RRsets, 64 values each. Only `allowed` and non-`deletionRequested` ZoneUnit items are rendered; cleanup ledger items are omitted, which is how a hosted record is "deleted".
- `contentDigest`: `sha256:` over the canonical wire form of `soa`, `nameServers`, and `records`, so servers and the withdrawal evaluator can bind evidence to exact content.

`status`:

- `servers[]`: map-list keyed by `endpoint` (pod identity `namespace/name`), each with `serial`, `contentDigest`, `observedAt`. Each server owns exactly its own item through server-side apply with field manager `hosted-server/<pod>`. Entries older than `2 × refresh` are pruned by the controller.

The snapshot is the provider's whole desired state. A server restarted from nothing reconstructs serving from snapshots alone; the controller restarted from nothing re-renders every `ZoneUnit` and converges serials without reading server state.

## Controller Responsibility

Per `ZoneUnit` with `spec.provider.name` equal to the bound provider name:

1. Validate the class parameters again against the storage schema; on failure write `Zone` `Accepted=False/InvalidParameters` and stop.
2. Render `records[]` from `spec.recordSets[]` (`allowed != false`, `deletionRequested == false`) plus apex NS and glue. Composition conflicts were already resolved by core; the renderer is total.
3. Compare `contentDigest` with the live `HostedZone`; if unchanged, do not write (no serial bump). Otherwise apply the snapshot with the next serial.
4. Report `ZoneUnit.status`:
   - `zone.nameServers` = class `nameServers[].name`.
   - `zone.conditions`: `Accepted=True/Accepted`; `Programmed=True/Programmed` once at least one `status.servers[]` item reports the current `serial` and `contentDigest`; otherwise `Programmed=False/ServingPending` with message `Snapshot serial <n> is not yet served by any hosted server.`; `Programmed=False/SnapshotRejected` when the API server rejects the snapshot (message bounded to 2048 bytes).
   - `zone.provider.data`: `serial`, `contentDigest`, `servers[]` (endpoint, serial, observedAt; at most 64), optional `delegation {observedNameServers[], matches, lastProbeTime}`.
   - `recordSets[]`: `Accepted=True/Accepted`; `Programmed` mirrors the zone-level serving state for the serial that first contained this item's generation; `deletionCompleted=true` for a `deletionRequested` item once a snapshot without it is served by at least one server.
5. Withdrawals: see below.

The controller watches `HostedZone` status changes (predicate on `status.servers[]`) so serving acknowledgement requeues the unit without polling.

Deletion of a `ZoneUnit` deletes the snapshot; servers stop answering for that apex (REFUSED) once the watch delivers the deletion. Retained withdrawal claims on a deleting unit are still verified.

## Hosted Server

`internal/go/providers/hosted/server` is a separate `main` target (`cmd/hosted-server`) built into the same image. It uses `github.com/miekg/dns` (already a dependency of the external probe) and no other DNS library.

Behavior:

- Listens on UDP and TCP `:5353` as a non-root process; the Service maps port 53. EDNS0 advertised size 1232; UDP answers that exceed the client's size are truncated with TC, and TCP is served in full.
- `AA=1`, `RA=0`. Queries whose name is not at or below a served apex get `REFUSED`. ANY is answered with `HINFO RFC8482`. AXFR/IXFR are `REFUSED` in this version; `NOTIFY` is not sent. Secondary transfer is a future extension through the snapshot, not through the server.
- Answers SOA and NS at the apex with glue in the additional section; A/AAAA/CNAME/TXT/MX/CAA/NS at owner names; in-zone CNAME chains are followed up to 8 links; wildcard synthesis follows RFC 4592 for the leftmost `*` label, with an existing closer name blocking synthesis. NXDOMAIN and NODATA carry the SOA in the authority section with TTL `min(soa.ttl, soa.minimum)`.
- Case-insensitive matching; 0x20 case of the question name is preserved in the answer.
- Memory and CPU are bounded by the CRD bounds (10000 RRsets × 64 values per zone, zones limited by `--max-zones`, default 4096). Per-source-address UDP rate limiting (`--udp-rate-limit`, default 1000 qps/source) protects against amplification; responses never exceed the request's advertised size over UDP.
- Each server watches `HostedZone` with an informer scoped by `--watch-namespace` (empty = all) and swaps an immutable in-memory zone index atomically per event. After loading a snapshot it applies its `status.servers[]` entry. On watch loss it keeps serving the last index and reports nothing until the watch is back.
- Metrics: `dns_hosted_queries_total{zone,qtype,rcode}`, `dns_hosted_query_duration_seconds`, `dns_hosted_zone_serial{zone}`, `dns_hosted_zones_loaded`, `dns_hosted_udp_rate_limited_total`.
- Health: `/healthz` liveness is process health; `/readyz` is true once the initial list completed. A server with zero zones is ready (it answers REFUSED, which is the correct authoritative behavior for an empty authority).

RBAC for the server ServiceAccount: `hostedzones` get/list/watch and `hostedzones/status` patch. Nothing else.

## Withdrawal Verification

Hosted advertises `recordWithdrawal.supported=true`. Exclusion is proven at the authority, which is stronger than external's resolver-view proof because the provider knows exactly what it serves:

```text
excluded(address) := address ∉ snapshot(records at checkedName, terminal A/AAAA)
                   ∧ ∀ view ∈ verification.views: probe(view, checkedName) shows address absent
```

The snapshot term is evaluated from the served snapshot (`status.servers[]` serial ≥ the first serial that omitted the address), not from the controller's intent. The probe term reuses the shared probe resolver (`internal/go/core/dnsprobe`, moved from `internal/go/providers/external/probe` with the external provider updated to import it; the package is a helper, not a provider abstraction). Negative TTL is `soa.minimum` by construction; `notBefore = t0 + max(retainedPositiveTTL, soa.minimum)`, and a confirming probe at or after `notBefore` is required. Conditions, reasons, messages, waiver echo, scheduling budget, and metrics (`dns_hosted_withdrawals_pending{reason}`) follow the external contract verbatim so platform consumers see one withdrawal vocabulary.

## Endpoint Conversion

App Provider capability `endpoint.hosted.dns.appthrust.io/v1alpha1`, resource `endpointrecordsetconversions`, with the same semantics as the external conversion: sorted canonical A then AAAA fragments or exactly one CNAME fragment, TTL from the class `defaultTTL`, rejection of mixed targets, multiple distinct hostnames, apex CNAME, and incompatible preferences. An `EndpointProviderCapability` manifest registers it.

## Admission and Security

- The core webhook validates parameters, adoption, and options from the Provider schema; `HostedZone` has its own CRD CEL (serial > 0, record name/type canonical forms, value counts).
- `HostedZone` spec writes are restricted by RBAC to the hosted controller ServiceAccount; a user cannot inject records by creating a snapshot because the controller owns every snapshot named after a `ZoneUnit` and deletes unowned ones (`SnapshotRejected` is reported on the unit whose name is shadowed).
- The server has no write path except its status entry and no recursion, so it cannot be used as an open resolver or amplifier beyond the bounded UDP response size.
- No credential, no outbound network except the withdrawal/delegation probes to configured endpoints.

## Packaging

Chart values:

| Value | Default | Effect |
| --- | --- | --- |
| `providers.hosted.enabled` | `false` | Controller registration, Provider/EndpointProviderCapability/APIService manifests, hosted RBAC, conversion handler. |
| `providers.hosted.controllerName` / `providerName` / `providerVersion` | contract defaults | Runtime binding. |
| `providers.hosted.server.replicas` | `1` | Hosted server Deployment. |
| `providers.hosted.server.service.type` | `ClusterIP` | `LoadBalancer` for a public VIP. |
| `providers.hosted.server.service.loadBalancerIP`, `.annotations`, `.ipFamilies` | unset | Deterministic VIP for glue. |
| `providers.hosted.server.watchNamespace` | `""` | Informer scope. |
| `providers.hosted.server.resources` | requests 50m/64Mi, limits 500m/256Mi | Bounded by zone limits. |

The CRD carrier gains `hostedzones.hosted.dns.appthrust.io`.

## Tests

- Unit: renderer (ZoneUnit → snapshot, digest stability, serial monotonicity, ledger omission), server (authoritative answers per RFC cases: apex SOA/NS/glue, NODATA, NXDOMAIN, wildcard with closer-name block, CNAME chain, TC/TCP, REFUSED, ANY, rate limit), withdrawal evaluator (snapshot ∧ view proof, `notBefore` from `soa.minimum`).
- Envtest: controller end to end with a fake server status writer: `ServingPending` → `Programmed`, `deletionCompleted` after omission is served, snapshot shadowing → `SnapshotRejected`.
- Local transport: real server process on a loopback port queried through the probe package; this is the same path the platform's delegation proof later exercises against a VIP.
- Chart: enablement gating renders nothing for hosted when disabled; APIService, RBAC, Deployment, and Service shapes when enabled.
