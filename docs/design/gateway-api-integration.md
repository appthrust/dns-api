# Gateway API Endpoint Integration

Gateway API integration is a DNS API App above Core. It watches Gateway API resources and creates common `EndpointRecordSet` resources. It does not create Core `RecordSet` resources directly.

The initial integration targets `HTTPRoute` and `Gateway` from `gateway.networking.k8s.io/v1`. Other route kinds are future work.

## Architecture

```text
HTTPRoute / Gateway
  -> gateway endpoint controller
  -> endpoint.dns.appthrust.io/EndpointRecordSet
  -> endpoint record set controller
  -> Provider EndpointRecordSetConversion API
  -> dns.appthrust.io/RecordSet
  -> dns.appthrust.io/ZoneUnit
  -> provider controller
```

The Gateway controller is a source controller. It reads Gateway API attachment state and emits endpoint publishing intent. The endpoint controller owns DNS-specific resolution and Core resource creation.

## Responsibilities

The Gateway controller:

- watches `HTTPRoute` and `Gateway`;
- waits until all `HTTPRoute.spec.parentRefs` are accepted;
- resolves hostnames from the route/listener relationship;
- reads targets from `Gateway.status.addresses`;
- creates one `EndpointRecordSet` for each accepted route/listener binding with usable hostnames and targets;
- deletes generated `EndpointRecordSet` resources when the route no longer produces them.

The Gateway controller does not:

- select `Zone`;
- select Core `Provider`;
- decide `A`, `AAAA`, or `CNAME`;
- decide TTL;
- call provider conversion APIs;
- create `RecordSet`;
- write provider-specific `adoption`.

The endpoint controller:

- watches `EndpointRecordSet`;
- selects matching Zones by longest suffix match;
- resolves `EndpointProviderCapability` from the selected Zone provider;
- calls the Provider `EndpointRecordSetConversion` API;
- validates returned fragments;
- checks `Zone.spec.allowedRecordSets`;
- creates and updates generated Core `RecordSet` resources.

Provider controllers still make final provider-side acceptance and programming decisions.

## EndpointRecordSet Creation

The Gateway controller creates one `EndpointRecordSet` per `HTTPRoute` parent/listener binding.

For a route:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  namespace: app
  name: web
spec:
  parentRefs:
    - namespace: platform
      name: public
      sectionName: https
  hostnames:
    - api.example.com
```

and a Gateway:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  namespace: platform
  name: public
spec:
  listeners:
    - name: https
      protocol: HTTPS
      port: 443
status:
  addresses:
    - type: Hostname
      value: k8s-public-123.ap-northeast-1.elb.amazonaws.com
```

the controller creates:

```yaml
apiVersion: endpoint.dns.appthrust.io/v1alpha1
kind: EndpointRecordSet
metadata:
  namespace: dns-api-system
  name: web-public-https-6b96e4d5f0
  labels:
    app.kubernetes.io/managed-by: dns-api-gateway-endpoint
    gateway.endpoint.dns.appthrust.io/route-namespace: app
    gateway.endpoint.dns.appthrust.io/route-name: web
    gateway.endpoint.dns.appthrust.io/gateway-namespace: platform
    gateway.endpoint.dns.appthrust.io/gateway-name: public
    gateway.endpoint.dns.appthrust.io/listener-name: https
spec:
  hostnames:
    - api.example.com
  targets:
    - type: Hostname
      value: k8s-public-123.ap-northeast-1.elb.amazonaws.com
```

The generated name is deterministic from:

```text
HTTPRoute namespace/name
Gateway namespace/name
listener name
```

Hostnames are not part of the object name, because changing route hostnames should update the same endpoint intent object rather than break ownership continuity.

## Hostname Resolution

For each accepted route/listener binding:

- If both `HTTPRoute.spec.hostnames` and listener `hostname` are set, use their intersection.
- If the route omits `spec.hostnames` and listener `hostname` is set, use the listener hostname.
- If the route sets `spec.hostnames` and listener `hostname` is omitted, use the route hostnames.
- If both omit hostnames, no `EndpointRecordSet` is created for that binding.

Wildcard intersection chooses the concrete hostname when one side is concrete and the other side is wildcard.

## Target Resolution

Targets come from `Gateway.status.addresses`.

Supported target types:

- `Hostname`
- `IPAddress`

The Gateway controller preserves target type and value. It does not turn hostname targets into `CNAME`, and it does not split IP targets into `A` and `AAAA`. That is provider conversion responsibility.

## Parent Acceptance

The initial behavior is conservative: DNS intent is generated only after every `HTTPRoute.spec.parentRefs[]` entry has an `Accepted=True` parent status.

This avoids partial DNS publication for a route whose complete Gateway attachment is not settled.

## Observability

`HTTPRouteDNSReport` is not part of the design. Gateway-specific reporting would be a projection and is not needed for the initial implementation.

Users inspect:

- generated `EndpointRecordSet` resources via Gateway labels;
- `EndpointRecordSet.status` for Zone selection, generated RecordSets, and conversion errors;
- generated `RecordSet.status` for Core and provider acceptance;
- `ZoneUnit.status` for zone-scoped provider state.

`EndpointRecordSet.status` is the common observability surface for Gateway, Ingress, Service, and future source Apps.

## Adoption and GitOps Restore

The Gateway controller does not write adoption. `EndpointRecordSet.spec` does not contain adoption.

Endpoint-based adoption and GitOps restore are not part of this design yet. Direct Core `RecordSet.spec.adoption` remains the supported adoption path.

## Provider Conversion

Gateway is provider-neutral. Provider-specific endpoint record shape belongs to the Endpoint Provider conversion API.

For Route 53:

- hostname targets that match known AWS load balancer suffixes become `A` and `AAAA` ALIAS fragments;
- IP targets become standard `A` and/or `AAAA` fragments;
- default TTL for standard IP records is chosen by the Route 53 conversion API;
- ALIAS records omit Core `ttl`;
- unsupported hostname targets fail conversion.

The endpoint controller receives `RecordSetSpecFragment[]`, attaches `zoneRef` and `provider`, and applies Core `RecordSet` resources.

## API Groups

Common endpoint resources:

```text
endpoint.dns.appthrust.io
```

Provider conversion implementation:

```text
endpoint.route53.dns.appthrust.io
```

Gateway source controller labels:

```text
gateway.endpoint.dns.appthrust.io/*
```
