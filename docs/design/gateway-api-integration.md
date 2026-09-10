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

- watches `HTTPRoute`, `Gateway`, and generated `EndpointRecordSet` lifecycle and desired-state changes;
- requires every `HTTPRoute.spec.parentRefs` entry to have a current `Accepted=True` condition before it creates a new hostname intent;
- resolves hostnames from the route/listener relationship;
- reads current targets from `Gateway.status.addresses`;
- creates one `EndpointRecordSet` for each Gateway with the union of currently admitted route hostnames and Gateway targets;
- preserves an already admitted hostname and its last targets only during transient status observation loss, using an internal ownership receipt on the managed `EndpointRecordSet`;
- updates or deletes generated `EndpointRecordSet` resources when routes, listeners, Gateway identity, admission, or hostname ownership no longer produce them.

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

The Gateway controller creates one `EndpointRecordSet` per Gateway. Multiple routes and listeners on the same Gateway are deduplicated into a single endpoint intent so HTTP and HTTPS routes for the same hostname do not create competing Core `RecordSet` owners.

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
  name: platform-public-4f93a40876
  labels:
    app.kubernetes.io/managed-by: dns-api-gateway-endpoint
    gateway.endpoint.dns.appthrust.io/gateway-namespace: platform
    gateway.endpoint.dns.appthrust.io/gateway-name: public
spec:
  hostnames:
    - api.example.com
  targets:
    - type: Hostname
      value: k8s-public-123.ap-northeast-1.elb.amazonaws.com
```

The generated name is deterministic from:

```text
Gateway namespace/name
```

Hostnames are not part of the object name, because changing route hostnames should update the same endpoint intent object rather than break ownership continuity.

## Hostname Resolution

For each accepted route/listener binding, the Gateway controller adds hostnames to the Gateway-owned `EndpointRecordSet`:

- If both `HTTPRoute.spec.hostnames` and listener `hostname` are set, use their intersection.
- If the route omits `spec.hostnames` and listener `hostname` is set, use the listener hostname.
- If the route sets `spec.hostnames` and listener `hostname` is omitted, use the route hostnames.
- If both omit hostnames, no `EndpointRecordSet` is created for that binding.

Wildcard intersection chooses the concrete hostname when one side is concrete and the other side is wildcard.

## Target Resolution

Targets normally come from the current `Gateway.status.addresses`.

The controller does not create a new hostname intent without nonempty current
addresses. A managed intent that was previously admitted may retain its
last-known nonempty targets while addresses are temporarily absent, but only
for hostnames whose internal receipt still matches the current route UID and
generation, Gateway UID and class, and exact parent/listener binding. A
Gateway deletion or replacement never reuses the former object's targets.

Supported target types:

- `Hostname`
- `IPAddress`

The Gateway controller preserves target type and value. It does not turn hostname targets into `CNAME`, and it does not split IP targets into `A` and `AAAA`. That is provider conversion responsibility.

## Parent Acceptance

The controller distinguishes current admission from unavailable observation for
each parent reference. A current `Accepted=True` condition has
`observedGeneration` equal to the route generation and is the only state that
can admit a hostname for a first publication. A current `Accepted=False`
explicitly withdraws the affected binding. Missing, `Unknown`, and stale
conditions are observation-unknown rather than denials: they may retain only
an exact receipt from an earlier admitted reconcile, never authorize a new
hostname.

An explicit current Gateway `Accepted=False` condition, whose
`observedGeneration` equals the Gateway generation, withdraws every hostname
for that Gateway. Missing, `Unknown`, and stale Gateway conditions do not
fabricate this revocation.

A later normal reconcile may create a fresh intent from current route admission
and current Gateway addresses after that withdrawal. It never carries the former
Gateway's targets or receipt forward.


An older managed `EndpointRecordSet` without a valid receipt fails closed during
unknown observation. It receives a receipt only from a normal reconcile with
complete current acceptance and nonempty current targets.


The receipt also records the specific listener. Hostname or parent removal,
listener removal or ownership change, route or Gateway identity replacement,
and route/Gateway deletion remove the affected hostname immediately. A
dynamic `allowedRoutes.namespaces.from: Selector` policy cannot be re-proved
without a namespace read, so unknown observation under that policy is not
retained. Independent sibling listener bindings are evaluated separately.

The receipt records route namespace and name only so a parent-reference removal,
change, deletion, or final not-found reconciliation can requeue the previously
bound Gateway. That discovery only schedules recomputation; it does not grant
authority or relax UID, generation, Gateway, listener, or hostname matching.

## Deletion and Recovery

An `EndpointRecordSet` may still be terminating when its route becomes accepted
again. The Gateway controller must not update that terminating intent or remove
its finalizers: provider cleanup belongs to the endpoint controller.

Generated endpoint deletion events enqueue the current routes referencing the
Gateway named by the endpoint's management labels. This relation uses a parent
Gateway index because the endpoint and Gateway may be in different namespaces
and cannot use a namespaced owner reference. Once deletion completes, accepted
routes recreate the intent without requiring another Route or Gateway change.
Routes that no longer satisfy publication requirements do not recreate it.

Endpoint status-only changes do not enqueue the source controller, and unchanged
endpoint spec and labels are not rewritten. Recovery therefore does not create a
self-sustaining write/watch loop; actual Gateway target or Route changes still
converge normally.

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

Endpoint-based adoption is controlled by the endpoint controller after Zone selection. For Route 53 migration, a Zone may opt generated records into `RecordSet.spec.adoption: {"enabled":true}` with `endpoint.dns.appthrust.io/route53-recordset-adoption=enabled`. This is intended for zones that already contain matching records from external-dns. New zones should leave the annotation unset so generated records are created normally.

Direct Core `RecordSet.spec.adoption` remains the supported path for hand-authored Core records.

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
