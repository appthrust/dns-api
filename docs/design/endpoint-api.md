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
            evaluateTargetHealth: false
      - type: AAAA
        name: api
        options:
          alias:
            dnsName: dualstack.k8s-public-123.ap-northeast-1.elb.amazonaws.com.
            hostedZoneID: Z14GRHDCWA56QT
            evaluateTargetHealth: false
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
