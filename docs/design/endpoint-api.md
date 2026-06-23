# Endpoint API

`endpoint.dns.appthrust.io` contains common DNS API App resources for publishing endpoint addresses into DNS. It is not Gateway-specific. Gateway, Ingress, Service, and future source Apps can reuse the same endpoint record set model and provider capability discovery.

## Endpoint RecordSet

An endpoint record set is a generated `A`, `AAAA`, or `CNAME` record candidate that publishes an endpoint address into DNS.

The v1 endpoint record set shape uses only these `RecordSet.spec` fields:

- `type`
- `name`
- `ttl`
- `a`
- `aaaa`
- `cname`
- `options`

It does not include `zoneRef`, `provider`, or `adoption`. The source App adds `zoneRef` and `provider` when it creates the final `RecordSet`. Endpoint record adoption is not supported in v1.

The v1 supported endpoint record set types are `A`, `AAAA`, and `CNAME`.

## EndpointProviderCapability

`EndpointProviderCapability` is a cluster-scoped discovery object that describes how one Core Provider version participates in endpoint record set Apps.

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
  defaults:
    ttl: 300
  refine:
    group: endpoint.route53.dns.appthrust.io
    resource: endpointrecordsetrefinereviews
```

`spec.provider` references a Core Provider version. `spec.provider.name` is `Provider.metadata.name`; `spec.provider.version` is `Provider.spec.versions[].name`.

`spec.defaults.ttl` is an optional static TTL default for source Apps that create endpoint record set candidates. The value must be in the core `RecordSet.spec.ttl` range, `1..2147483647`. Source Apps apply this value before calling the refine API, unless the source workflow provides an explicit TTL override.

`spec.refine` is optional. Simple providers that can accept provider-neutral endpoint record sets may omit it. Providers that need provider-specific record shaping, such as Route 53 ALIAS records or provider-specific TTL/options, declare `spec.refine`.

When `spec.refine` is omitted, the source App uses the endpoint record set candidate after applying `spec.defaults`. This static path is valid only when the resulting endpoint record set can be converted directly to a dns-api `RecordSet.spec` and accepted by the Core Provider. If admission later rejects the generated `RecordSet`, the source App reports the admission failure and does not keep an invalid generated object.

The effective refine GVR is:

```text
group = spec.refine.group
version = spec.refine.version, or spec.provider.version when omitted
resource = spec.refine.resource
```

`spec.refine.group` and `spec.refine.resource` are required when `spec.refine` is present. `spec.refine.version` is optional and must be non-empty when present. `spec.refine.resource` is the plural lowercase API resource name used by the Kubernetes API, such as `endpointrecordsetrefinereviews`.

Endpoint API admission validates `EndpointProviderCapability` independently of endpoint record sets. It validates TTL range and static field shape, but it does not check whether the referenced Core Provider exists or whether the corresponding `APIService` is currently available. Runtime availability is reported by the source App as `EndpointRecordSetRefineUnavailable`.

For a given Core Provider `name` and `version`, at most one `EndpointProviderCapability` may be effective. Source Apps treat multiple matching capabilities as invalid configuration and report `EndpointProviderCapabilityConflict`.

## Endpoint RecordSet Refine API

The Endpoint RecordSet Refine API is a create-only review API served by an App Provider aggregated API server. It is not a CRD and review objects are not persisted. Kubernetes authentication, authorization, audit, API discovery, and APIService routing are handled by the Kubernetes API server.

The Provider package owns the aggregated API server and the matching `APIService` registration for its refine API. A source App does not create `APIService` resources and does not know the backing Service name, serving certificate, or CA bundle. It reads `EndpointProviderCapability.spec.refine`, then uses a Kubernetes client to `create` that resource through the Kubernetes API server.

The shared review shape is:

```yaml
apiVersion: endpoint.route53.dns.appthrust.io/v1alpha1
kind: EndpointRecordSetRefineReview
request:
  uid: 5f0f0b5e-0000-0000-0000-000000000001
  recordSet:
    type: CNAME
    name: api
    ttl: 300
    cname:
      target: dualstack.k8s-public-123.ap-northeast-1.elb.amazonaws.com
response:
  uid: 5f0f0b5e-0000-0000-0000-000000000001
  result:
    status: Success
    reason: Refined
    message: refined endpoint record set
    retryable: false
  recordSet:
    type: A
    name: api
    options:
      alias:
        dnsName: dualstack.k8s-public-123.ap-northeast-1.elb.amazonaws.com.
        hostedZoneID: Z14GRHDCWA56QT
        evaluateTargetHealth: true
```

`request.recordSet` and `response.recordSet` have the same endpoint record set shape.

The API accepts one endpoint record set candidate per request and returns one refined endpoint record set. If a provider needs multiple record sets for one endpoint, such as routing policy records, v1 reports unsupported behavior. Multi-record-set refine is future work.

The refine API may change `type`, `ttl`, record body fields, and `options`. It must not change `name`, and it must not add `adoption`. The `name` is derived from the source hostname and selected Zone; changing it would make `allowedRecordSets` checks and report output ambiguous.

`response.uid` must equal `request.uid`. `response.result.status` is `Success` or `Failure`. On `Success`, `response.recordSet` is required. On `Failure`, `response.recordSet` is ignored.

Failure mapping:

- `EndpointRecordSetRefineUnsupported`: no matching `EndpointProviderCapability`, no supported static path, and no refine API.
- `EndpointProviderCapabilityConflict`: multiple matching `EndpointProviderCapability` resources exist for the same Core Provider name and version.
- `EndpointRecordSetRefineUnavailable`: retryable failure, timeout, APIService unavailability, discovery failure, or 5xx response.
- `EndpointRecordSetRefineDenied`: RBAC `Forbidden` or authentication failure.
- `EndpointRecordSetRefineInvalid`: successful API call returned malformed content, UID mismatch, missing successful `recordSet`, changed `name`, unsupported response type, unexpected `adoption`, or schema-invalid provider payload.
- `EndpointRecordSetRefineFailed`: non-retryable failure response from the refine API.

The App Provider refine API owns provider-specific record shaping. For example, Route 53 refines a hostname `CNAME` endpoint record set candidate into an `A` record with `options.alias` when the hostname matches a supported AWS alias target and the target hosted zone ID is known. Cloudflare may return a `CNAME` endpoint record set and set provider-owned options. Source Apps must not contain provider-name-specific branches for Route 53 ALIAS or Cloudflare options.

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
