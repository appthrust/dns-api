# DEP-0003: Verified record withdrawal before address reuse

- Status: Draft
- Owner: dns-api maintainers (Core, Endpoint App, and external provider)
- Created: 2026-10-18

## Summary

Introduce independent Core `RecordWithdrawal` and `RecordWithdrawalWaiver` claims, Core composition/projection through `ZoneUnit`, and external-provider exclusion verification. Endpoint sources preserve retired hostname/address pairs until the Endpoint App durably creates claims. Platform consumers use verified results before member retirement and address reuse; exceptional operator risk acceptance remains distinct from DNS verification.

This is the dns-api part of the reviewed **Phase 4b: verified external-DNS withdrawal before VIP reuse** design, especially §§3.2–3.4, 4.1–4.3, 6–7, 8.2, and 9. It is a follow-up after PR #16, not additional stage-1 implementation scope. It is proposed behavior, not an implementation or runtime-verification report. Under the [DEP process](../docs/design/common.md#dep-process), committed `docs/design/` remains current specification until acceptance and reflection there.

## Motivation

The external provider observes customer-managed DNS; it neither owns nor deletes those DNS records. Ordinary generated-child deletion can finish while the customer's wildcard or exact record still resolves to a retired address. Reusing that address can route traffic for the former owner to a new allocation.

The stage-1 baseline is [External Provider §Deletion](../docs/design/external-provider.md#deletion): matching-UID `DeletionRequested`/`DeletionCompleted` is Kubernetes bookkeeping, not proof of DNS withdrawal. Its deferred `Withdrawn` sentence explicitly leaves schema, persistence, consumers, and cache safety outside stage 1. On acceptance, this DEP supersedes that deferral, **not** ordinary RecordSet deletion semantics.

A result tied only to an ERS or generated RecordSet disappears when that object is deleted. Provider-private scheduling state is reconstructible and pruned, not a durable public obligation inventory. Independent Core claims preserve the obligation across source deletion and keep App → Core → Provider ownership intact.

### Source anchors

Anchors below are from proposal source snapshot `00f98491d24f57fa5af66c805ccbb5c4fabd5aef`; line numbers describe that revision, not future edits:

| Existing contract | Source anchor |
| --- | --- |
| Core composes and projects; provider reconciles Units | `docs/design/controllers-webhooks.md:5-28`; `docs/design/overview.md:13-18` |
| Unit ledger identities and deletion bookkeeping | `pkg/go/api/dns/v1alpha1/zoneunit_types.go:60-134,184-218`; `internal/go/core/controllers/zoneunit/zoneunit_controller.go:459-465,515-531` |
| External deletion and pruned scheduling state | `internal/go/providers/external/controllers/zoneunit/controller.go:179-187,217-223`; `model.go:16-27,102-113`; `docs/design/external-provider.md:264-268` |
| Source update/delete paths, including route cleanup | `internal/go/apps/gatewayendpoint/controllers/gateway/controller.go:219-245,345-402,406-420` |
| Endpoint finalizer installation and deletion | `internal/go/apps/endpoint/controllers/endpointrecordset/controller.go:63-84,536-559` |
| Nonempty A/AAAA validation | `pkg/go/api/dns/v1alpha1/recordset_types.go:87-97`; `internal/go/core/webhook/validator.go:450-481` |
| Exact-set evaluator and minimum-TTL summaries | `internal/go/providers/external/probe/evaluate.go:117-230`; `types.go:45-56`; `wire.go:267-270,297-305` |
| Zone child-claim deletion boundary | `internal/go/core/controllers/zoneunit/zoneunit_controller.go:217-255` |
| DEP process and reflection rules | `docs/design/common.md:7-21,41-49` |

## Goals

- Verify exclusion of one retired address while permitting healthy replacement addresses.
- Preserve obligations through concurrent source edits, direct deletion, and controller restarts.
- Keep authorization/composition/public status in Core and DNS verification in the provider.
- Expose identity-bound, cache-aware, reverified results and an independently authorized waiver.
- Preserve external stage-1 programming/deletion and non-external behavior.

## Non-Goals

- DNS mutation, customer credentials, a second address allocator, or automatic release after a timeout/attempt count.
- Empty public A/AAAA RecordSets, publication epochs, platform DNS-history inventories, or an ERS admission webhook.
- Enumeration of deleted-route history or proof that every historical customer hostname/cache is clear.
- IPv6 platform allocation: dns-api accepts IPv4 and IPv6 withdrawals, but the current platform member allocator remains IPv4-only.

The supported proof covers the Project wildcard plus currently retained ERS hostnames, retirement entries, and their independent claims. Wildcards use stable concrete sampled witnesses, not exhaustive descendant enumeration. An explicit owner no longer represented by retained state is outside this proof. [INFERENCE] Configured resolvers/authorities are trusted and honor TTLs; arbitrary application caches, unconfigured views, and future customer edits are outside the guarantee.

## API

### RecordWithdrawal

Namespaced GVK: `dns.appthrust.io/v1alpha1`, kind `RecordWithdrawal`.

| Surface | Proposed contract |
| --- | --- |
| `spec.zoneRef` | Existing Core Zone reference shape; immutable. |
| `spec.name` | Canonical zone-relative owner: `@`, exact name, or leftmost wildcard; immutable. |
| `spec.address` | One canonical IPv4 or IPv6 address to exclude; immutable; no hostname target. |
| `metadata.name` | Deterministic creator key. Endpoint identity includes ERS UID, retirement generation, owner, and address; platform identity includes reservation UID, wildcard owner, and address. Collision handling checks complete immutable shape and source provenance. |
| `metadata.labels[endpoint.dns.appthrust.io/address]` | Required from both creators. IPv4 uses its canonical string; IPv6 uses lowercase unpadded base32 of the 16 normalized bytes. Validation checks label/spec consistency. |
| Source annotations | Creator/source namespace/name/UID/generation for diagnosis and collision checks, never authorization. Core does not interpret platform/Gateway kinds. |
| Ownership/lifecycle | No ownerReferences and no request-level finalizer. Operator-controlled DNS namespace; no age-based garbage collection. |

Public status includes:

- `observedGeneration`, resolved `zone` reference/UID and Unit identity;
- `policyFingerprint`, `requiredViews`, `checkedNames`, and `sampledWildcard`;
- `absentSince`, `notBefore`, `lastProbeTime`, `nextProbeTime`, and `verifiedAt`;
- `resultDigest`, covering claim UID/generation/spec, Unit/binding identity, policy, checked names/views, and observation window/result;
- bounded `views[]`/provider evidence and diagnostics, without using a truncated preview for decisions;
- `waiverRef` with exact accepted waiver name/UID/digest and reservation UID, when present;
- `Accepted`, `Withdrawn`, `Ready`, and optional `OverrideAccepted` conditions, each with observedGeneration.

Creators supply neither resolver policy nor `spec.verifier`. Address-label selection is only discovery: consumers recheck canonical spec address, UID, expected source/wildcard identity, and result bindings. There is no reservation-UID label requirement, consumerRef propagation, Gateway metadata patch, or platform-kind lookup inside dns-api. Platform uses bounded live APIReader label queries; truncation/continuation or missing expected results holds release, never proves an empty inventory.

### ZoneUnit and Provider contract

`ZoneUnit.spec.withdrawals[]` is a map-list bounded to 256 items and keyed by the scalar top-level fields `claimNamespace` and `claimName`. Each item also contains a nested `claim{namespace,name,uid}` object; CEL keeps its namespace/name consistent with the top-level keys, and its UID is nonempty. The item carries observedGeneration, relative name, address, Core authorization/Allowed state, and any Core-validated waiver reference and immutable scope. Core applies the same Zone namespace and record-pattern grants as for corresponding A/AAAA RecordSets; wildcard grants remain explicit.

`ZoneUnit.status.withdrawals[]` uses the same scalar map-list keys and 256-item bound, mirrors the nested claim identity with CEL-enforced namespace/name consistency, and carries verifier conditions, effective policy fingerprint, checked names/views, cache window, resultDigest, and waiver binding. Core projects only exact UID/generation matches using `claimNamespace`, `claimName`, and `claim.uid` for identity, and preserves provider messages. Same-name recreation cannot inherit an old result; missing, stale, or disallowed items are not ready.

Provider versions expose a `recordWithdrawal` capability containing `supported`, public status-provider-data schema, and `additionalConditionReasons`. External advertises support; Route53/Cloudflare do not advertise or reconcile it. Unsupported binding is a Core condition. The provider reconciles only matching ZoneUnits/ZoneClasses: it does not read App objects, withdrawal claims, or waivers, or duplicate grant authorization. Core is the sole public withdrawal/waiver status writer.

Pending withdrawals do not make unrelated active RecordSet/Zone `Programmed` false. Zone deletion has a separate dependency gate.

### RecordWithdrawalWaiver

Namespaced GVK: `dns.appthrust.io/v1alpha1`, kind `RecordWithdrawalWaiver`, in its target's operator-controlled namespace. It is an immutable separate resource, not a privileged field on the withdrawal claim, and has no ownerReferences or request-level finalizer.

Its spec contains exact withdrawal namespace/name/UID and immutable claim-spec digest; canonical address; exact receiving reservation namespace/name/UID; reason; ticket/change reference; and `acceptStaleDNSRisk: true`.

Kubernetes **create RBAC is the authority**. The spec contains no client-supplied authorizedBy, username, group, authorization time, or claimed audit identity. Kubernetes API audit supplies authenticated identity/time/provenance; persisted UID/resourceVersion identifies the authorization.

Core checks reference/UID/digest/address bindings and composes a validated waiver into the corresponding Unit item. The provider binds the exact waiver UID and emits `OverrideAccepted=True/OperatorAuthorizedRelease`; Core projects it. No provider claim reads or SubjectAccessReview are introduced. A waiver **never sets `Withdrawn=True`**; an old DNS answer remains visible as `Withdrawn=False/DNSStillResolves`.

Every unresolved obligation needs its exact waiver. Another reservation incarnation or recreated/changed claim cannot consume it. Waivers bypass DNS absence/cache proof only, not active/uncaptured ERS targets, allocation ownership, workload/storage drain, or host cleanup. Deleting a waiver does not undo an already consumed release. Claims/waivers remain retained after reservation deletion.

### Conditions and messages

| Condition/status | Reason | Meaning |
| --- | --- | --- |
| `Accepted=True` | `Accepted` | Binding, grants, and capability accepted. |
| `Accepted=False` | `InvalidWithdrawal`, `NotAllowed`, `UnsupportedProvider` | Invalid shape/binding, denied grant, or unavailable capability; no release. |
| `Withdrawn=False`, `Ready=False` | `DNSStillResolves` | A required checked name/view/family still returns the retired address. |
| `Withdrawn=False`, `Ready=False` | `NegativeTTLNotElapsed` | Exclusion observed; cache horizon or separate confirming attempt remains. |
| `Withdrawn=Unknown`, `Ready=False` | `DNSProbeFailed` | Missing, failed, incomplete observation or required negative SOA evidence. |
| `Withdrawn=Unknown`, `Ready=False` | `PolicyChanged` | Binding/view-policy change cannot be equivalent or weaker success. |
| `Withdrawn=True`, `Ready=True` | `WithdrawalVerified` | Complete current all-view exclusion after horizon and confirming probe. |
| `OverrideAccepted=True` | `OperatorAuthorizedRelease` | Separately authorized exact waiver, not successful DNS observation. |

Stable message templates:

- `DNS <checkedName> still resolves to retired address <address> in <view> (<qtype>).`
- `Retired address <address> is absent in every required view; waiting until <notBefore> for cache safety and a confirming probe.`
- `Retired address <address> was verified absent from <name> in every required view at <verifiedAt>.`
- `External DNS withdrawal cannot be verified for <name> in <view>: <closedFailureCode>.`

Use the existing 2048-byte message limit and bounded sorted diagnostics; include no packets, credentials, or unbounded address lists. Use **result**/`resultDigest`; existing ERS admission **receipt** semantics remain unchanged. The provider describes DNS, not whether ERS desires the address, and does not emit an ERS-derived `EndpointStillPublished` condition.

## Behavior

### Source capture and endpoint lifecycle

The Gateway source remains provider-neutral: no Zone/provider selection, resolver inspection, or Core claim creation. It preserves removed pairs in two correlated ERS collections:

- `spec.retiredTargets[]`: entry ID, retirement source generation, removed IP, and its old hostname snapshot.
- `spec.retiredHostnames[]`: entry ID, retirement source generation, removed hostname, and its old IP snapshot.

Each capture key includes ERS UID/generation. Associations are snapshots, not independent sets later multiplied together: simultaneous removal of H1 and change V1→V2 retains `(H1,V1)`, never invented `(H1,V2)`. Duplicate pairs across collections share deterministic claim identity; a later re-add/remove generation has a new retirement identity.

Both collections use `listType=map` keyed by entry ID. Limits are 64 target-retirement entries and 256 hostname-retirement entries, with snapshots capped at 256 hostnames and 64 IPs respectively. Overflow reports `WithdrawalCapturePending` and leaves old intent intact; it never truncates or drops obligations.

1. The source reads current ERS, computes removed pairs, and atomically changes desired intent while appending snapshots, preserving existing retirement entries with resourceVersion optimistic concurrency.
2. Source writes never prune. The endpoint controller resolves the Zone/provider for each retired pair and creates external Core claims by deterministic create-or-get, checking complete immutable shape/provenance on collision.
3. Only after every associated claim is durably created/read back does endpoint prune exact captured entry IDs using an optimistic-lock patch. Current-generation status confirms captured entries. A create failure leaves retirement intent; a crash between create and prune is harmless. Source retries merge current state, never restore an old whole spec over concurrent entries.
4. Non-external pairs finish existing child cleanup and prune without an external DNS wait. Generated RecordSets/ERSs can finish ordinary deletion once independent claims exist; they do not wait for customer DNS changes.

Missing/deleting Gateway, rejected admission, and every cleanup path, **including `cleanupForRoute`**, move remaining pairs to retired state rather than immediately delete the ERS. Source deletion waits for current-generation capture confirmation.

The source installs endpoint-owned `generatedRecordSetsFinalizer` **at ERS create**, not first endpoint reconcile. Direct ERS DELETE runs `reconcileDeletion`, captures every live and retired pair, then removes that finalizer after normal child cleanup. Other enabled sources follow the same create/retire contract or cannot publish into the protected external member-address path. Controller outage cannot lose retained intent.

A retire-only ERS may have no active hostname/target but nonempty retirement entries; CEL permits that transitional state. Endpoint reports `Ready=False/Retiring` and creates no empty RecordSet or conversion request. Active ERSs retain nonempty-intent validation. No ERS validating webhook, protection-label authorization, or new failurePolicy dependency is added; trusted source RBAC and monotone writes enforce capture. Manual stripping of finalizers, deletion of retained evidence, or unauthorized overwriting of protected state is outside the supported contract.

### Exclusion verification and cache safety

`EvaluateWithdrawal` is separate from existing exact-set `Evaluate`:

```text
excluded(address) := address ∉ normalize(terminal A ∪ terminal AAAA)
```

For every required checked name and view, probe **both families**, even for IPv4 retirement. One old answer blocks; missing/failed/incomplete required evidence is Unknown. Replacement addresses may remain and need not be identical across views. No partial-observation union, subset, or resolver failover constitutes success.

- Complete NOERROR with other addresses is exclusion evidence.
- Complete NODATA and NXDOMAIN are exclusion evidence only with required valid negative SOA evidence. Active `Evaluate` still returns `DNSNameNotFound` for NXDOMAIN.
- Follow existing CNAME eight-link/loop and foreign-chain boundaries and inspect both terminal families. Loops, limits, mixed invalid data, or unresolved chains are Unknown.
- Required views include every configured Recursive server or effective Cluster resolver and every required discovered authority endpoint when expectedNameservers is configured. Existing NS/SOA/AA/delegation checks remain.

Retain complete per-view/per-qtype expiry evidence; existing minimum-TTL summaries alone are insufficient:

1. First complete all-view exclusion starts `absentSince=t0`.
2. For each negative response, negative TTL is `min(SOA TTL, SOA.MINIMUM)`; missing required SOA is not invented zero.
3. `notBefore` is the maximum of t0, every `t0 + negativeTTL`, and retained known positive-answer/CNAME expiry horizons. Platform drain delay remains additional.
4. Identical negative answers do not slide t0 indefinitely. A changed bound may extend but never shorten the window. Reappearance, failed/incomplete observation, policy change, or stale scheduling invalidates/restarts success; old positive-expiry evidence remains conservative.
5. A **new complete successful probe at/after notBefore** is mandatory, even with zero negative TTL. Timer firing or restart without fresh evidence cannot set `Withdrawn=True`.
6. Restart preserves a window only with matching claim/binding identity, policy/view set, and fresh schedule; otherwise restart conservatively. A 3600-second TTL is not shortened to the polling cap. A replacement with no negative responses invents no negative TTL but still honors old-positive horizons and a separate confirming probe.

Retained claims are reverified. Later failure drops readiness. Consumers reject future/missing timestamps and stale success at `now > nextProbeTime + 300s`, and require matching UID/generation/binding/policy/digest and `verifiedAt >= notBefore`.

A live Class/policy change invalidates success/window. The prior required view set remains a floor: a weaker policy reports `PolicyChanged` until restored or explicitly waived. Missing historical policy evidence is Unknown, not proof of equivalence. Creators do not supply copied policy snapshots.

For wildcard claims the verifier derives a stable concrete witness from claim UID and bound Zone identity, reports `sampledWildcard=true`, and reuses it across probes/restarts. Recreated claims get distinct witnesses. A literal `*` query alone is insufficient; retained exact hostnames get their own claims. Closer enclosers/explicit owners can override a witness, so the result remains sampled.

Reuse [Scheduling and Resource Bounds](../docs/design/external-provider.md#scheduling-and-resource-bounds): shared limiter, bounded concurrency/deadlines, fair work, jitter, timers, stale-input checks, and QUERY-only transport. There is no maximum hold duration or release-after-N-attempts. Customer DNS continuing to answer the old IP holds indefinitely with `DNSStillResolves` and visible probe times. Finite sampling does not promise global cache eradication.

### Core retention and Zone deletion

Core retains outstanding withdrawals as dependent Zone claims. Zone/ZoneUnit deletion stays blocked while retained claims reference it, with a visible blocking dependency; the external provider continues verification instead of treating Unit deletion as successful withdrawal. Completed claims remain retained/reverified and can also pin deletion until an operator safely archives/removes them after no allocated address/reservation can consume them. There is no age-based GC. Missing claims, CRDs, bindings, or force-deleted Units never imply success.

### Admission and RBAC

Admission validates local shape, immutable spec/references, canonical name/address, address-label consistency, bounded schemas, condition/status shape, and existing namespace-policy declarations. Cross-object binding/grants/provider capability are Core reconciliation conditions. Admission performs no DNS/network query or field-level SubjectAccessReview. Ordinary nonempty A/AAAA and CNAME/ownership rules remain unchanged.

| Actor | Permission boundary |
| --- | --- |
| Platform CapacityReservation | Create/get/list/watch withdrawals in configured DNS namespace; read/list/watch waiver/results and configured Zone binding; existing ERS reads. No public claim status writes, waiver create, or general RecordSet mutation. |
| Platform CRS | Read/list/watch claims/waivers and reservation status; no claim create or public status writes. |
| Gateway source | Existing ERS create/update/patch/delete; install finalizer and preserve retired entries. No withdrawal, ZoneClass, or resolver permissions. |
| Endpoint controller | Create/get/list/watch withdrawals, existing Zone resolution and ERS spec/status/finalizer permissions. No waiver create or withdrawal status writes. |
| Core | Read/watch both claims, compose Unit spec, write public claim statuses, retain Zone dependencies. |
| External provider | Matching Unit/Class reconciliation and Unit status only; no App/withdrawal/waiver reads or public claim status writes. |
| Operator release role | Namespace-scoped waiver create/get/list/watch; API audit retention records privileged authorization. |
| Tenant/Project administrator | No mutation of retained operator-namespace claims, statuses, or safety metadata. |
| Operator archive role | Explicit safe deletion of completed, nonconsumable evidence; never an automatic release mechanism. |

Chart/Kustomize permissions and registrations agree. New Core CRDs, Unit/ERS schemas, generated bindings, admission/controller registrations, and provider capability ship together in both `app/operator/config/crd/bases/` and `deploy/charts/dns-api/crds/`. No customer Secret/cloud identity privilege is added.

## Compatibility

Stage-1 external RecordSet `DeletionRequested`/`DeletionCompleted` and ordinary finalizer bookkeeping stay unchanged and separate. The new withdrawal items survive source deletion and continue probing; their Zone deletion dependency is intentionally new. Existing active exact-set evaluation and admission receipt terminology remain unchanged. `none`/Route53 do not gain external DNS waiting; writing providers do not advertise the capability.

Two claim creators are intentional: platform CapacityReservation creates the mandatory Project wildcard per retiring external allocation, even with zero routes/ERSs; endpoint creates retained ERS-pair claims. Both use the same Core contract, neither writes status. Platform gates both CRS retirement and terminal-reservation address clearing on current results; a waiver has a separate release reason. External has no automatic `NeverPublished` release because customer wildcard DNS can exist before any route.

The dns-api Core follow-up release, reviewed platform import, and CRD delivery precede platform Phase 4b support. Missing CRD/watch/verifier, denied/truncated reads, missing/stale results, or a mode switch cannot erase held external obligations. Pre-4b retirements that already lost history are not reconstructed by epochs or synthetic success.

## Documentation

On acceptance, reflect this contract in present tense into:

- `docs/design/external-provider.md` §Deletion, conditions/public data, scheduling, and admission/security, replacing the deferred `Withdrawn` sentence while preserving stage-1 deletion bookkeeping;
- `docs/design/core-api.md`: both claims, Unit lists, immutable fields, status, and Zone deletion dependency;
- `docs/design/controllers-webhooks.md`: Core admission/authorization/composition/projection and ownership;
- `docs/design/provider-extension.md`: capability, schema, and condition reasons;
- `docs/design/endpoint-api.md`: correlated bounded retirement state, retire-only validation, capture acknowledgment, and finalizer lifecycle;
- `docs/design/gateway-api-integration.md`: monotone writes, finalizer at create, and all cleanup paths.

The document writer subsequently updates user-facing manuals from those committed design snapshots: proof limitations, indefinite holds, safe archival, exact waiver procedure, and audit retention. Draft publication does not modify current design, root user README, or manuals.

## Alternatives

- **Provider-private retained ZoneUnit ledger:** feasible with public result projection and Zone retention, but changes reconstructible scheduling state into primary durable claim inventory. It still needs monotone source capture and exclusion verification. Independent Core claims make identity, authorization, and lifecycle explicit without hiding safety inventory in provider JSON.
- **Platform-owned history and empty RecordSet:** rejected. A/AAAA and ERS/conversion fragments are nonempty today; relaxing admission changes the ordinary model. Exact empty-set equality rejects a healthy replacement IP, whereas exclusion permits it; active NXDOMAIN evaluation is also not withdrawal. It duplicates DNS binding/grant/lifecycle logic in platform and asynchronous lastPublished watches can miss overwritten pairs.
- **ERS admission webhook:** rejected. It introduces a new all-write availability/failurePolicy and authorization dependency. Trusted monotone source writers, create-time endpoint finalization, and create-before-prune retain intent without a second admission convention.
- **Publication epochs/history-recovery classes:** rejected. They do not repair already-lost history and add another protocol beyond allocation provenance and UID/generation-fenced claims. Mandatory zero-route wildcard claims and currently retained correlated ERS pairs define the supported proof; no claimed historical completeness is manufactured.

## Tests

These are implementation requirements, not executed results of this documentation change.

### Unit and controller coverage

- Separate exact-set/exclusion behavior; replacements allowed; NXDOMAIN differs for publication; NODATA/missing SOA, zero/nonzero TTL, both families, and IPv4-mapped normalization.
- Every recursive/authority view, opposite-family old answer, complete CNAME terminal lookup, loop/eight-link limits, partial fan-out, and failed required view.
- Maximum negative/known-positive horizons, 3600-second TTL not capped, repeated negatives not sliding forever, mandatory separate confirming probe, restart/clock reversal/stale scheduling/late-result invalidation, policy weakening, UID recreation, and old-IP reappearance.
- Stable sampled wildcard witness; explicit retained hostname blocks after wildcard removal; zero routes still has platform wildcard obligation; proof limitation remains visible.
- Correlated simultaneous hostname/target changes, duplicate pairs, re-add/remove generations, append/prune conflict, create-before-prune crash/failure, overflow hold, and endpoint outage.
- Every source cleanup including `cleanupForRoute`, create-time finalizer, and direct DELETE capture of live and retired pairs.
- Core grant denial/wildcard grant, unsupported capability, exact claim/Unit UID-generation projection, message preservation, provider ownership boundaries, and pending withdrawal not breaking active programming.
- Zone deletion remains blocked and probes continue; ordinary RecordSet deletion bookkeeping stays unchanged.
- Canonical IPv4/IPv6 label encoding and exact-spec validation; label-only match never grants success.
- Waiver role allowed; tenant/endpoint/platform denied; exact immutable claim/reservation UID/digest/address binding; DNS False persists under accepted override; audit identity is never client-controlled.

### Cross-controller and controlled acceptance

Use regenerated real CRDs, admission, Provider declaration, endpoint/Core/external controllers, role-based denials, and controlled DNS rather than injecting a projected success shortcut. Pause/interleave move-to-retired, create/readback, status capture, and prune: live readers see retained intent or persisted claims, never neither. Cover missing prerequisites, bounded/truncated lists, stale True, policy changes, retained-source disappearance, and exact waiver consumption.

The Phase 4b controlled fixture has two eligible sshd-container hosts, one workload VIP, and Projects A/B. A's wildcard and retained exact names keep answering the VIP after source retirement; projected `DNSStillResolves` blocks CRS and allocation beyond the old local drain timer. B passes host selection but fails specifically with `LoadBalancerRangeExhausted`, not machine exhaustion. A verifier restart preserves the hold.

Customer DNS removal leaves one required view answering the VIP first, then all views supply valid exclusion/SOA evidence. Assert no release before the maximum cache horizon, followed by a new confirming all-view probe, Core `WithdrawalVerified`, the additional platform drain delay, ordinary member cleanup, and automatic terminal-reservation release. Recreate B's failed deployment through the normal path, then prove actual VIP reuse and serving without overlapping allocations. Capture wildcard-owner and independent non-witness evidence as well as the stable sampled witness and retained exact names. Compare unrelated management DNS/allocations and clean disposable resources through ordinary lifecycles without deleting unresolved evidence.

Additional cases cover zero-route wildcard publication, finished reservation/member already gone (hosts clean but VIP held), replacement address still serving, both-family/partial-address behavior, and a separate exceptional waiver run with DNS still False. Waiver acceptance is never substituted for the normal verified-withdrawal proof.
