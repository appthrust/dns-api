# Endpoint API

`endpoint.dns.appthrust.io` contains common DNS API App resources for publishing endpoint addresses into DNS. It is not Gateway-specific. Gateway, Ingress, Service, and future source Apps create `EndpointRecordSet` resources. The endpoint controller resolves those resources into Core `RecordSet` resources.

## Endpoint RecordSet

`EndpointRecordSet` is a persistent endpoint publishing intent. It says which DNS hostnames should point at which endpoint targets. It does not choose a DNS Provider, Zone, record type, TTL, or provider-specific options.

Example:

```yaml
apiVersion: endpoint.dns.appthrust.io/v1alpha1
kind: EndpointRecordSet
metadata:
  namespace: app
  name: web-public-https
spec:
  hostnames:
    - api.example.com
  targets:
    - type: Hostname
      value: k8s-public-123.ap-northeast-1.elb.amazonaws.com
```

The source App owns source-specific interpretation. For example, the Gateway App reads `HTTPRoute` and `Gateway`, then creates one `EndpointRecordSet` for each Gateway that has accepted routes with usable hostnames and targets. The source App should use labels or owner references for source tracking. `EndpointRecordSet.spec` does not include `sourceRef` because the endpoint controller does not use it to reconcile DNS.

The Service source App reads annotated `LoadBalancer` Services and creates one
`EndpointRecordSet` from the requested hostnames and `status.loadBalancer.ingress`
targets. New Service DNS intent should use
`endpoint.dns.appthrust.io/hostnames`; the legacy
`external-dns.alpha.kubernetes.io/hostname` annotation is accepted only as a
migration source so clusters can remove external-dns before every Service
manifest has been rewritten.

`EndpointRecordSet.spec` contains:

- `hostnames`: fully qualified DNS hostnames to publish.
- `targets`: endpoint addresses. The initial target types are `Hostname` and `IPAddress`.
- `retiredTargets`: map-list keyed by `id`, at most 64 entries, each containing `sourceGeneration`, removed canonical IP `address`, and a nonempty snapshot of at most 256 old `hostnames`.
- `retiredHostnames`: map-list keyed by `id`, at most 256 entries, each containing `sourceGeneration`, removed `hostname`, and a nonempty snapshot of at most 64 old IP `addresses`.

Retirement IDs bind the ERS UID, pre-update generation, entry kind, and removed value. Snapshots preserve old associations: simultaneous removal of H1 and replacement V1→V2 retains `(H1,V1)`, not `(H1,V2)`. Sources append and preserve existing entries with resourceVersion concurrency; only the endpoint controller prunes captured intent.

CEL requires either nonempty live hostnames **and** targets, or a nonempty retirement collection (`nonempty hostname and target intent or retirement entries are required`). A retire-only object may omit either live side. There is no ERS admission webhook; trusted source writes enforce the retirement protocol.

It does not contain:

- `provider`
- `zoneRef`
- `type`
- `ttl`
- `a`, `aaaa`, or `cname`
- `options`
- `adoption`

Those fields are selected or produced later by the endpoint controller and Provider conversion API.

## Endpoint Controller

The endpoint controller reconciles `EndpointRecordSet` resources:

1. Resolve each hostname to candidate Core `Zone` resources by longest suffix match.
2. Use the selected Zone's Core `Provider` reference.
3. Find the matching `EndpointProviderCapability`.
4. Create a provider-specific `EndpointRecordSetConversion` through the Kubernetes API server.
5. Receive `RecordSetSpecFragment` output from the Provider.
6. Attach `zoneRef`, `provider`, and any Zone-enabled adoption policy, then apply Core `RecordSet` resources.

The endpoint controller does not decide provider-specific record shape. It does not hard-code Route 53 alias hosted zone IDs or Cloudflare TTL behavior.

Generated `RecordSet.metadata.name` uses the endpoint name, a normalized relative
record name, the lowercase record type, and a ten-character SHA-256 suffix. The
zone apex `@` becomes `apex` in the object name; `*` remains `wildcard` and `.`
remains `-`. `RecordSet.spec.name` keeps the original DNS name, including `@` or
`*`. The hash still includes the original relative name and endpoint/Zone
identity, so the apex and a literal `apex` label remain distinct. Existing valid
object names and the 63-character truncation scheme are unchanged.

Before applying children, the controller checks each generated object name
against Kubernetes RFC 1123 subdomain validation. An invalid name produces
`Resolved=False` with reason `InvalidRecordSetName` for that hostname and no
children for it; other hostnames continue reconciling. A hostname without an
accepted Zone still reports `ZoneNotResolved`. Either case makes the aggregate
`Resolved` condition false with reason `HostnameNotResolved`, rather than
preventing valid hostnames from publishing. Kubernetes read/write errors still
return reconciliation errors.

### Aggregate readiness and deletion

`EndpointRecordSet.status.conditions` contains generation-aware `Accepted`,
`Programmed`, and `Ready` summaries. `Ready=True` requires every generated
`RecordSet` to have current `status.observedGeneration` and current
`Accepted=True` and `Programmed=True` conditions. Missing, empty, `Unknown`, or
prior-generation child status is not ready. Each generated child status exposes
both its current metadata `generation` and mirrored `observedGeneration` so a
consumer can independently verify the aggregate.

Sources install `endpoint.dns.appthrust.io/generated-recordsets` at ERS creation, before first endpoint reconciliation; the endpoint controller also ensures it on existing active objects. This closes the source-create/direct-delete capture gap.

Before pruning retirement entries or deleting generated children, the endpoint controller resolves each retired hostname/IP pair to its retained publication Zone binding, or the ordinary single-pair publication resolution/conversion/fallback path. For a Provider version supporting withdrawal it durably creates a deterministic independent `RecordWithdrawal`; successful Create is persisted evidence, and AlreadyExists requires Get plus exact immutable shape, lifecycle, address label, and provenance checks. Duplicate pairs at the same retirement generation share a claim. Missing resolution, collisions, write failures, and collection overflow hold capture rather than discard intent. A known non-supporting Provider requires no withdrawal claim.

Active ERSs clear captured retirement collections with an optimistic-lock patch; a concurrent append conflicts instead of being lost. Retire-only ERSs retain entries until DELETE and report current-generation `WithdrawalCaptured=True/Captured`, message `Every retired hostname/address pair has been durably captured.` Their `Resolved`, `Accepted`, `Programmed`, and `Ready` conditions are `False/Retiring`, message `EndpointRecordSet is retiring; no active records are published.` They issue no empty conversion request or RecordSet. Capture failure reports `WithdrawalCapturePending` and `Ready=False`; source overflow leaves the old spec unchanged and is never truncated.

`WithdrawalProtected` defaults closed: only current-generation `False` after all hostname providers resolve without withdrawal support permits the old unprotected cleanup path. Once True, protection is sticky for that ERS incarnation. `WithdrawalCaptured=True` is accepted only at the current ERS generation. Capture acknowledgment proves independent obligation persistence, not DNS withdrawal.

Direct ERS deletion captures every live IP/hostname pair and all retirement entries before normal child deletion. The endpoint finalizer is removed only after a label-selected read proves no generated RecordSet remains. Capture/provider cleanup failures retain finalizers. Independent withdrawal claims survive parent deletion and do not delay ordinary deletion while the customer changes DNS.

## EndpointProviderCapability

`EndpointProviderCapability` is a cluster-scoped discovery object that describes how one Core Provider version participates in endpoint Apps.

Example:

```yaml
apiVersion: endpoint.dns.appthrust.io/v1alpha1
kind: EndpointProviderCapability
metadata:
  name: route53-v1alpha1
spec:
  provider:
    name: route53.dns.appthrust.io
    version: v1alpha1
  conversion:
    group: endpoint.route53.dns.appthrust.io
    resource: endpointrecordsetconversions
```

`spec.provider` references a Core Provider version. `spec.provider.name` is `Provider.metadata.name`; `spec.provider.version` is `Provider.spec.versions[].name`.

`spec.conversion` identifies the provider-specific create-only conversion API. The effective conversion GVR is:

```text
group = spec.conversion.group
version = spec.conversion.version, or spec.provider.version when omitted
resource = spec.conversion.resource
```

For a given Core Provider `name` and `version`, at most one `EndpointProviderCapability` may be effective. The endpoint controller treats multiple matching capabilities as invalid configuration and reports `EndpointProviderCapabilityConflict`.

## Endpoint RecordSet Conversion API

`EndpointRecordSetConversion` is a create-only API served by an App Provider aggregated API server. It is not persisted. Kubernetes authentication, authorization, audit, API discovery, and APIService routing are handled by the Kubernetes API server.

The Provider package owns the aggregated API server and matching `APIService` registration. The endpoint controller reads `EndpointProviderCapability.spec.conversion`, then creates that resource through the Kubernetes API server.

Example:

```yaml
apiVersion: endpoint.route53.dns.appthrust.io/v1alpha1
kind: EndpointRecordSetConversion
spec:
  uid: 5f0f0b5e-0000-0000-0000-000000000001
  input:
    hostname: api.example.com
    name: api
    zone:
      domainName: example.com
    targets:
      - type: Hostname
        value: dualstack.k8s-public-123.ap-northeast-1.elb.amazonaws.com
status:
  uid: 5f0f0b5e-0000-0000-0000-000000000001
  result:
    status: Success
    reason: Converted
    message: converted endpoint record set
  output:
    fragments:
      - type: A
        name: api
        options:
          alias:
            dnsName: dualstack.k8s-public-123.ap-northeast-1.elb.amazonaws.com.
            hostedZoneID: Z14GRHDCWA56QT
            evaluateTargetHealth: true
      - type: AAAA
        name: api
        options:
          alias:
            dnsName: dualstack.k8s-public-123.ap-northeast-1.elb.amazonaws.com.
            hostedZoneID: Z14GRHDCWA56QT
            evaluateTargetHealth: true
```

`spec.input` is an `EndpointRecordSetConversionInput`: one hostname from an `EndpointRecordSet`, the selected zone-relative record name, the selected Zone domain name, and endpoint targets. `status.output.fragments[]` is a non-empty list of `RecordSetSpecFragment`: Provider-converted fragments of `RecordSet.spec` before `zoneRef` and `provider` are attached.

`RecordSetSpecFragment` contains `type`, `name`, `ttl`, `a`, `aaaa`, `cname`, and `options`. It does not contain `zoneRef`, `provider`, or `adoption`; the endpoint controller adds `zoneRef`, `provider`, and Zone-enabled adoption policy after selecting the Zone. The conversion API may choose record type, TTL, record body fields, and options. It must not change `name`.

`status.uid` must equal `spec.uid`. `status.result.status` is `Success` or `Failure`. On `Success`, `status.output.fragments` is required and must have at least one item. On `Failure`, `status.output` is ignored.

Failure mapping:

- `EndpointRecordSetConversionUnsupported`: no matching `EndpointProviderCapability` or no provider-supported output.
- `EndpointProviderCapabilityConflict`: multiple matching `EndpointProviderCapability` resources exist for the same Core Provider name and version.
- `EndpointRecordSetConversionUnavailable`: retryable failure, timeout, APIService unavailability, discovery failure, or 5xx response.
- `EndpointRecordSetConversionDenied`: RBAC `Forbidden` or authentication failure.
- `EndpointRecordSetConversionInvalid`: successful API call returned malformed content, UID mismatch, missing or empty successful fragments, changed `name`, unsupported response type, or schema-invalid provider payload.
- `EndpointRecordSetConversionFailed`: non-retryable failure response from the conversion API.

## ZoneUnit and Adoption

`ZoneUnit` is the Core ownership registry. `EndpointRecordSet.spec` and `RecordSetSpecFragment` do not contain adoption.

The endpoint controller may attach provider-specific adoption only when the selected `Zone` explicitly opts in. For Route 53, setting the Zone annotation `endpoint.dns.appthrust.io/route53-recordset-adoption=enabled` makes generated `RecordSet.spec.adoption` equal `{"enabled":true}`. This is a migration control for hosted zones that already contain matching records created by another controller such as external-dns. New zones should omit the annotation so generated records are created normally.

## API Groups

Endpoint common resources use:

```text
endpoint.dns.appthrust.io
```

Endpoint App Provider implementations use provider-specific API groups:

```text
endpoint.route53.dns.appthrust.io
endpoint.cloudflare.dns.appthrust.io
```

Gateway-specific endpoint automation uses:

```text
gateway.endpoint.dns.appthrust.io
```
