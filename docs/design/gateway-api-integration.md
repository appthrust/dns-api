# Gateway API Integration

dns-api provides a Gateway API integration that observes `HTTPRoute` and `Gateway` resources and reports how their DNS intent maps to dns-api `Zone` and `RecordSet` resources.

The initial integration targets `HTTPRoute` and `Gateway` from `gateway.networking.k8s.io/v1`. Other Route kinds such as `TLSRoute`, `TCPRoute`, `UDPRoute`, and `GRPCRoute` are out of scope for the initial version.

Gateway API integration is a DNS API App. It runs above Core, derives `RecordSet` resources from Gateway API state, and reports the result through `HTTPRouteDNSReport`. It must not expand Core controller responsibility. It uses the common Endpoint API in `endpoint.dns.appthrust.io`; provider-specific endpoint record set shaping is exposed through `EndpointProviderCapability` and the Endpoint RecordSet Refine API.

## Problem Space

Without Gateway API integration, an application team must create or update at least three resource areas to attach a hostname to a Gateway:

1. A `Gateway` exists with listeners and provider-assigned addresses.
2. An `HTTPRoute` attaches application traffic to that Gateway.
3. A dns-api `RecordSet` points the hostname to the Gateway address.

This creates several failure modes:

- A route exists but DNS is never created.
- A route is deleted or changed but old DNS remains.
- DNS points at the wrong Gateway address.
- Operators must inspect `Gateway.status.addresses` manually and translate it to provider-specific DNS target data, such as a Route 53 `ALIAS` target for an AWS Load Balancer Controller Gateway.

The affected users are both application teams and platform teams. Application teams want `HTTPRoute` to be the normal place where hostnames are declared. Platform teams want DNS to remain constrained by dns-api `Zone`, `ZoneClass`, provider identity, and `allowedRecordSets` policy.

external-dns already handles a similar flow with its Gateway API source: it derives DNS endpoints from `HTTPRoute` and `Gateway`, then provider implementations choose the matching provider zone. dns-api follows the same broad source interpretation, but it must expose intermediate feedback because dns-api creates Kubernetes `RecordSet` resources before provider reconciliation. A missing `Zone`, an `allowedRecordSets` rejection, or an unsupported target shape should be visible as Kubernetes status, not only as controller logs.

## Goals

- Observe `HTTPRoute` and `Gateway` resources and derive DNS hostnames from their attachment relationship.
- Create dns-api `RecordSet` resources for supported `HTTPRoute` hostname targets.
- Report DNS translation, policy, and programming state in a readable Kubernetes resource.
- Make missing zones, parent acceptance problems, unsupported target shapes, and `allowedRecordSets` denials visible without reading controller logs.
- Keep the integration compatible with the existing dns-api `Zone`, `RecordSet`, and provider controller model.

## Non-Goals

- Do not change Gateway API resources or write `HTTPRoute.status`.
- Do not implement Gateway or HTTPRoute admission validation.
- Do not support `TLSRoute`, `TCPRoute`, `UDPRoute`, or `GRPCRoute` initially.
- Do not add Route 53 weighted, latency, failover, or other routing policy support as part of the initial Gateway integration.
- Do not expose a user-authored desired-state API for Gateway DNS records in the initial version.
- Do not add route or Gateway selector configuration in the initial version.

## API

The integration adds a report resource:

```yaml
apiVersion: gateway.endpoint.dns.appthrust.io/v1alpha1
kind: HTTPRouteDNSReport
metadata:
  namespace: app
  name: web
```

`HTTPRouteDNSReport` is a controller-owned report for one `HTTPRoute`. It is created in the same namespace and with the same name as the `HTTPRoute`. The controller sets an owner reference to the `HTTPRoute`, so the report is deleted with the route.

The resource is read-only for application users. It is not a user-authored API. The initial type has no meaningful `spec`; all useful data is written under `status`.

Example:

```yaml
apiVersion: gateway.endpoint.dns.appthrust.io/v1alpha1
kind: HTTPRouteDNSReport
metadata:
  namespace: app
  name: web
status:
  routeRef:
    namespace: app
    name: web
  hostnames:
    - hostname: api.example.com
      zone:
        ref:
          namespace: platform-dns
          name: example-com
        domainName: example.com
        conditions:
          - type: Selected
            status: "True"
            reason: LongestAllowedSuffixMatch
      recordSets:
        - type: A
          name: api
          options:
            alias:
              dnsName: dualstack.example-alb.ap-northeast-1.elb.amazonaws.com.
              hostedZoneID: Z14GRHDCWA56QT
              evaluateTargetHealth: true
          conditions:
            - type: Resolved
              status: "True"
              reason: Resolved
            - type: Allowed
              status: "True"
              reason: AllowedByZone
            - type: Created
              status: "True"
              reason: RecordSetCreated
          generated:
            ref:
              namespace: dns-api-system
              name: gwrs-8f3a2c91d7-a
            conditions:
              - type: Accepted
                status: "True"
                reason: Accepted
              - type: Programmed
                status: "False"
                reason: ProviderChangePending
        - type: AAAA
          name: api
          options: {}
          conditions:
            - type: Resolved
              status: "False"
              reason: UnsupportedTarget
    - hostname: denied.example.com
      zone:
        candidates:
          - ref:
              namespace: platform-dns
              name: example-com
            domainName: example.com
            reason: RecordSetNotAllowed
        conditions:
          - type: Selected
            status: "False"
            reason: RecordSetNotAllowed
      recordSets:
        - type: A
          name: denied
          conditions:
            - type: Resolved
              status: "True"
              reason: Resolved
            - type: Allowed
              status: "False"
              reason: RecordSetNotAllowed
            - type: Created
              status: "False"
              reason: RecordSetNotCreated
    - hostname: api.unknown.example.net
      zone:
        conditions:
          - type: Selected
            status: "False"
            reason: ZoneNotResolved
      recordSets: []
  conditions:
    - type: Resolved
      status: "False"
      reason: PartialFailure
    - type: Programmed
      status: "False"
      reason: RecordSetNotProgrammed
```

The exact status field names may change during implementation, but the status model stays route-scoped and grouped by DNS hostname. The first question the report answers is: "for this `HTTPRoute`, which `RecordSet` objects is the controller trying to create for each hostname, and which Zone was selected?" Policy and generated `RecordSet` details are attached under each `recordSets[]` item. Parent, listener, and Gateway address details are included only when they explain a blocked or unsupported `RecordSet`.

## Controller Scope

The initial controller watches all `HTTPRoute` and `Gateway` resources in the cluster. It does not support route namespace filters, route label filters, route annotation filters, Gateway namespace filters, Gateway name filters, or Gateway label filters in the initial version.

Filtering is future work. The initial design keeps the controller behavior simple and closer to a cluster-level integration. Operators can rely on dns-api `Zone` and `allowedRecordSets` policy to decide which hostnames can become `RecordSet` resources.

The controller creates an `HTTPRouteDNSReport` when an `HTTPRoute` has at least one Gateway parent reference and a DNS hostname can be derived from route hostnames, Gateway listener hostnames, or their intersection. If the route has no Gateway parent references, the controller does not create a report.

The initial implementation uses one Gateway DNS controller. It is responsible for both:

- reading `HTTPRoute`, `Gateway`, `Zone`, and generated `RecordSet` resources and writing `HTTPRouteDNSReport.status`;
- creating, updating, and deleting generated `RecordSet` resources.

`HTTPRouteDNSReport` is not a controller-to-controller desired-state boundary in the initial version. It is a user-facing report produced by the Gateway DNS controller.

## Hostname Resolution

The controller resolves DNS hostnames from the relationship between `HTTPRoute.spec.hostnames` and matching Gateway listeners.

Rules:

- If both the route and listener define hostnames, the controller uses their hostname intersection.
- If the route omits `spec.hostnames` and the listener defines `hostname`, the listener hostname is used.
- If the route defines `spec.hostnames` and the listener omits `hostname`, the route hostname is used.
- If both omit hostnames, no DNS hostname is produced.
- Wildcards follow Gateway API source behavior used by external-dns: a wildcard label such as `*.example.com` is a suffix match, and the more specific overlapping hostname is used.

Examples:

```yaml
# Gateway listener hostname
hostname: api.example.com

# HTTPRoute hostnames omitted
```

produces `api.example.com`.

```yaml
# Gateway listener hostname
hostname: api.example.com

# HTTPRoute hostname
hostnames:
  - "*.example.com"
```

produces `api.example.com`.

```yaml
# Gateway listener hostname
hostname: "*.example.com"

# HTTPRoute hostname
hostnames:
  - api.example.com
```

produces `api.example.com`.

## Hostname Examples

### HTTPRoute Hostname Only

When `HTTPRoute.spec.hostnames` is set and the Gateway listener does not set `hostname`, the route hostname becomes the DNS hostname.

Input:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  namespace: platform
  name: public
spec:
  gatewayClassName: alb
  listeners:
    - name: https
      protocol: HTTPS
      port: 443
      allowedRoutes:
        namespaces:
          from: All
status:
  addresses:
    - type: Hostname
      value: dualstack.example-alb.ap-northeast-1.elb.amazonaws.com
---
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
  rules:
    - backendRefs:
        - name: web
          port: 8080
status:
  parents:
    - parentRef:
        namespace: platform
        name: public
        sectionName: https
      controllerName: example.com/gateway-controller
      conditions:
        - type: Accepted
          status: "True"
          reason: Accepted
```

Expected report shape:

```yaml
apiVersion: gateway.endpoint.dns.appthrust.io/v1alpha1
kind: HTTPRouteDNSReport
metadata:
  namespace: app
  name: web
status:
  hostnames:
    - hostname: api.example.com
      zone:
        ref:
          namespace: platform-dns
          name: example-com
        domainName: example.com
      recordSets:
        - type: A
          name: api
          options:
            alias:
              dnsName: dualstack.example-alb.ap-northeast-1.elb.amazonaws.com.
              hostedZoneID: Z14GRHDCWA56QT
              evaluateTargetHealth: true
```

### Gateway Listener Hostname Only

When `HTTPRoute.spec.hostnames` is omitted and the Gateway listener sets `hostname`, the listener hostname becomes the DNS hostname.

Input:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  namespace: platform
  name: public
spec:
  gatewayClassName: alb
  listeners:
    - name: https-api
      protocol: HTTPS
      port: 443
      hostname: api.example.com
      allowedRoutes:
        namespaces:
          from: All
status:
  addresses:
    - type: Hostname
      value: dualstack.example-alb.ap-northeast-1.elb.amazonaws.com
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  namespace: app
  name: web
spec:
  parentRefs:
    - namespace: platform
      name: public
      sectionName: https-api
  rules:
    - backendRefs:
        - name: web
          port: 8080
status:
  parents:
    - parentRef:
        namespace: platform
        name: public
        sectionName: https-api
      controllerName: example.com/gateway-controller
      conditions:
        - type: Accepted
          status: "True"
          reason: Accepted
```

Expected report shape:

```yaml
apiVersion: gateway.endpoint.dns.appthrust.io/v1alpha1
kind: HTTPRouteDNSReport
metadata:
  namespace: app
  name: web
status:
  hostnames:
    - hostname: api.example.com
      zone:
        ref:
          namespace: platform-dns
          name: example-com
        domainName: example.com
      recordSets:
        - type: A
          name: api
          options:
            alias:
              dnsName: dualstack.example-alb.ap-northeast-1.elb.amazonaws.com.
              hostedZoneID: Z14GRHDCWA56QT
              evaluateTargetHealth: true
```

### HTTPRoute And Gateway Listener Hostnames

When both the route and listener set hostnames, the DNS hostname is their intersection. If one side is a wildcard and the other is concrete, the concrete hostname is used.

Input:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  namespace: platform
  name: public
spec:
  gatewayClassName: alb
  listeners:
    - name: https-api
      protocol: HTTPS
      port: 443
      hostname: api.example.com
      allowedRoutes:
        namespaces:
          from: All
status:
  addresses:
    - type: Hostname
      value: dualstack.example-alb.ap-northeast-1.elb.amazonaws.com
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  namespace: app
  name: wildcard
spec:
  parentRefs:
    - namespace: platform
      name: public
      sectionName: https-api
  hostnames:
    - "*.example.com"
  rules:
    - backendRefs:
        - name: web
          port: 8080
status:
  parents:
    - parentRef:
        namespace: platform
        name: public
        sectionName: https-api
      controllerName: example.com/gateway-controller
      conditions:
        - type: Accepted
          status: "True"
          reason: Accepted
```

Expected report shape:

```yaml
apiVersion: gateway.endpoint.dns.appthrust.io/v1alpha1
kind: HTTPRouteDNSReport
metadata:
  namespace: app
  name: wildcard
status:
  hostnames:
    - hostname: api.example.com
      zone:
        ref:
          namespace: platform-dns
          name: example-com
        domainName: example.com
      recordSets:
        - type: A
          name: api
          options:
            alias:
              dnsName: dualstack.example-alb.ap-northeast-1.elb.amazonaws.com.
              hostedZoneID: Z14GRHDCWA56QT
              evaluateTargetHealth: true
```

### Multiple HTTPRoute Hostnames

When an `HTTPRoute` has multiple hostnames and the Gateway listener does not narrow them, the report contains one hostname entry for each route hostname. Each hostname can have its own Zone and `recordSets`.

Input:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  namespace: platform
  name: public
spec:
  gatewayClassName: alb
  listeners:
    - name: https
      protocol: HTTPS
      port: 443
      allowedRoutes:
        namespaces:
          from: All
status:
  addresses:
    - type: Hostname
      value: dualstack.example-alb.ap-northeast-1.elb.amazonaws.com
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata:
  namespace: app
  name: multi
spec:
  parentRefs:
    - namespace: platform
      name: public
      sectionName: https
  hostnames:
    - api.example.com
    - admin.example.com
  rules:
    - backendRefs:
        - name: web
          port: 8080
status:
  parents:
    - parentRef:
        namespace: platform
        name: public
        sectionName: https
      controllerName: example.com/gateway-controller
      conditions:
        - type: Accepted
          status: "True"
          reason: Accepted
```

Expected report shape:

```yaml
apiVersion: gateway.endpoint.dns.appthrust.io/v1alpha1
kind: HTTPRouteDNSReport
metadata:
  namespace: app
  name: multi
status:
  hostnames:
    - hostname: api.example.com
      zone:
        ref:
          namespace: platform-dns
          name: example-com
        domainName: example.com
      recordSets:
        - type: A
          name: api
          options:
            alias:
              dnsName: dualstack.example-alb.ap-northeast-1.elb.amazonaws.com.
              hostedZoneID: Z14GRHDCWA56QT
              evaluateTargetHealth: true
    - hostname: admin.example.com
      zone:
        ref:
          namespace: platform-dns
          name: example-com
        domainName: example.com
      recordSets:
        - type: A
          name: admin
          options:
            alias:
              dnsName: dualstack.example-alb.ap-northeast-1.elb.amazonaws.com.
              hostedZoneID: Z14GRHDCWA56QT
              evaluateTargetHealth: true
```

## Parent Acceptance

`HTTPRoute` can reference multiple parents. Gateway API reports acceptance per parent in `HTTPRoute.status.parents[]`; acceptance is not a single route-wide condition.

The dns-api Gateway integration treats all Gateway parents referenced by the route as part of the route's DNS intent. The initial version requires all Gateway parents that contribute to a hostname to be accepted before creating `RecordSet` resources for that hostname. If one parent is accepted and another is rejected or unresolved, the report records a blocked condition and does not create partial DNS.

This avoids silently routing DNS to only a subset of the route's declared parents.

## Endpoint Capability

The Gateway DNS controller uses the common Endpoint API in `endpoint.dns.appthrust.io`. A selected Zone's Core Provider version is compatible with the initial Gateway API integration when there is exactly one matching `EndpointProviderCapability`.

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

`spec.defaults.ttl` is an optional static TTL default for endpoint record set candidates. `spec.refine` is optional. When present, it identifies the Endpoint RecordSet Refine API. The Provider package owns the aggregated API server and `APIService` registration for that API. The Gateway DNS controller reads `EndpointProviderCapability`, creates the configured review resource through the Kubernetes API server, and never calls the backing Service directly.

The refine API version defaults to `EndpointProviderCapability.spec.provider.version` when `spec.refine.version` is omitted. For the example above, the Gateway DNS controller calls:

```text
POST /apis/endpoint.route53.dns.appthrust.io/v1alpha1/endpointrecordsetrefinereviews
```

If no matching `EndpointProviderCapability` exists, the report records `EndpointRecordSetRefineUnsupported` and no generated `RecordSet` is created for that Provider. If multiple matching capabilities exist, the report records `EndpointProviderCapabilityConflict`.

## Endpoint RecordSet Resolution

Accepted Gateway parents contribute targets from `Gateway.status.addresses`. The initial target types are:

- `IPAddress`
- `Hostname`

The controller first converts Gateway addresses into provider-neutral endpoint record set candidates:

| Gateway address | Endpoint record set candidate |
| --- | --- |
| IPv4 `IPAddress` | `A` with `a.addresses[]` |
| IPv6 `IPAddress` | `AAAA` with `aaaa.addresses[]` |
| `Hostname` | `CNAME` with `cname.target` |

An endpoint record set is a generated `A`, `AAAA`, or `CNAME` record candidate that publishes an endpoint address into DNS. It uses a subset of `RecordSet.spec` fields: `type`, `name`, `ttl`, `a`, `aaaa`, `cname`, and `options`. It does not include `zoneRef`, `provider`, or `adoption`; `zoneRef` and `provider` are added by the Gateway DNS controller after Provider refine succeeds, and endpoint record set adoption is not supported in v1.

The Gateway DNS controller constructs each endpoint record set candidate from:

- the zone-relative record name;
- the Gateway address type and value;
- `EndpointProviderCapability.spec.defaults.ttl`, when present;
- a valid `dns.appthrust.io/ttl` annotation value, when present.

The controller does not include `adoption` in endpoint record sets. Endpoint record set adoption is not supported in v1.

When the matching `EndpointProviderCapability` declares `spec.refine`, the controller sends the candidate to the Endpoint RecordSet Refine API:

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
```

The Provider returns one refined endpoint record set:

```yaml
response:
  uid: 5f0f0b5e-0000-0000-0000-000000000001
  result:
    status: Success
    reason: Refined
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

The Gateway DNS controller validates that a successful response has the same `uid`, contains one endpoint record set, preserves the input `name`, omits `adoption`, and uses a supported endpoint record set type. It then adds `zoneRef` and `provider` to create the final dns-api `RecordSet`.

Provider-specific record shaping belongs to the Endpoint RecordSet Refine API, not to the Gateway DNS controller. For example, Route 53 may refine a hostname `CNAME` candidate into an `A` record with `options.alias`, and Cloudflare may keep it as `CNAME` while setting provider-owned options. Route 53 hosted zone ID lookup for ALB or NLB alias targets is part of the Route 53 refine API implementation. The Gateway DNS controller must not hard-code AWS alias suffix tables or Cloudflare option rules.

The Gateway DNS controller stops only when it cannot create an endpoint record set candidate, refine it when required, or convert the resulting endpoint record set to a dns-api `RecordSet`. It does not duplicate every provider-specific acceptance rule. Once a resulting endpoint record set can be expressed as a dns-api `RecordSet` and is allowed by dns-api policy, the existing core and provider controllers decide whether the `RecordSet` is accepted and programmed. For example, the Gateway DNS controller does not reject a Cloudflare apex `CNAME` on its own; it creates the `RecordSet` and projects the resulting `RecordSet.status.conditions` into the report.

If the same resolved DNS hostname has multiple Gateway targets, the initial controller reports `MultipleGatewayTargetsUnsupported` and does not create a `RecordSet`. This is necessary because a single Route 53 `ALIAS` record set cannot express multiple alias targets without Route 53 routing policy such as weighted records and `setIdentifier`. Routing policy support is future work.

## TTL Resolution

Gateway API does not define a TTL field for `HTTPRoute` or `Gateway`. The Gateway DNS controller therefore resolves TTL before creating generated `RecordSet` resources.

`RecordSet.spec.ttl` is optional in the core CRD schema, but provider validation may require it. For example, dns-api provider schemas commonly use `require-ttl` for standard record bodies. Some provider-specific refined shapes may omit or replace core TTL:

- Route 53 `options.alias` records omit `spec.ttl`.
- Cloudflare `options.ttl: Auto` records omit `spec.ttl`.

The initial Gateway DNS controller supports an optional `HTTPRoute` annotation override:

```yaml
metadata:
  annotations:
    dns.appthrust.io/ttl: "300"
```

The annotation value is parsed as seconds. The accepted range is the core `RecordSet.spec.ttl` range, `1..2147483647`. Invalid values are reported on the `HTTPRouteDNSReport` and no invalid `RecordSet` is created.

TTL resolution rules:

- If `dns.appthrust.io/ttl` is present and valid, the controller sets the endpoint record set candidate's `ttl` to that value before Provider refine.
- If the annotation is absent and the matching `EndpointProviderCapability` has `spec.defaults.ttl`, the controller sets the endpoint record set candidate's `ttl` to that value before Provider refine.
- If neither the annotation nor `spec.defaults.ttl` supplies TTL, the endpoint record set candidate omits `ttl`.
- The Endpoint RecordSet Refine API may return a refined record that omits `ttl` or uses provider-owned TTL options when that is the correct provider-specific shape.
- If the annotation is invalid, the report records `TTLNotResolved` and the controller does not call refine for that candidate.
- If the refined endpoint record set is rejected later by `RecordSet` admission because no accepted TTL shape is present, the report records the admission failure and no invalid generated `RecordSet` is kept.

`EndpointProviderCapability.spec.defaults.ttl` is a narrow Endpoint App default. Core `Provider` does not carry Endpoint App defaults. Provider-specific automatic TTL behavior, such as Cloudflare automatic TTL, is not inferred by the Gateway DNS controller; it must be returned by the Endpoint RecordSet Refine API when needed.

## Zone Selection

The controller selects a dns-api `Zone` by matching each resolved DNS hostname against existing `Zone.spec.domainName` values.

The basic rule follows external-dns provider behavior:

- Candidate zones are zones whose domain name equals the hostname or is a suffix of the hostname.
- The most specific candidate is selected by longest matching domain name.

dns-api adds one more selection constraint: the refined generated `RecordSet` must be allowed to attach to the selected `Zone`.

Endpoint record set handling depends on the candidate Zone because the Zone selects the Core Provider version, and the Core Provider version selects a matching `EndpointProviderCapability`. For each suffix-matching candidate Zone, the controller builds the zone-relative endpoint record set candidate, applies `EndpointProviderCapability.spec.defaults`, calls the refine API when `spec.refine` is present, then checks whether the resulting generated `RecordSet` in the configured RecordSet namespace is allowed by `Zone.spec.allowedRecordSets`. This includes:

- generated `RecordSet` namespace
- zone-relative record name
- refined record type

If the longest matching zone does not allow the generated `RecordSet`, the controller may continue to the next less-specific matching zone. The selected zone is the most specific matching zone that also allows the generated `RecordSet`. If no matching zone allows the refined generated `RecordSet`, the report records a denial and no `RecordSet` is created.

Generated `RecordSet` resources are written to the controller-configured `recordSetNamespace`, such as `dns-api-system`. This namespace is not derived from the `HTTPRoute` namespace and does not have to match the controller Pod namespace. Platform teams must grant `recordSetNamespace` through `Zone.spec.allowedRecordSets` for shared zones.

Condition reasons:

- `ZoneNotResolved`: no `Zone` matches the hostname.
- `RecordSetNotAllowed`: matching zones exist, but none allows the generated `RecordSet` namespace, name, and type.

## RecordSet Management

The controller creates dns-api `RecordSet` resources only after the report data is resolvable and supported.

Creation requirements for a hostname:

- A DNS hostname is resolved from the route/listener relationship.
- All contributing Gateway parents are `Accepted=True`.
- Gateway addresses are present.
- A `Zone` is selected.
- Exactly one `EndpointProviderCapability` matches the selected Zone's Core Provider name and version.
- The controller can build an endpoint record set candidate from the Gateway address.
- If `EndpointProviderCapability.spec.refine` is present, the Endpoint RecordSet Refine API returns one successful refined endpoint record set.
- The resulting endpoint record set can be converted to the current dns-api `RecordSet.spec`.
- `Zone.spec.allowedRecordSets` allows the refined generated `RecordSet`.
- The generated `RecordSet` passes Kubernetes and dns-api admission.

Generated `RecordSet` resources are created in `recordSetNamespace`, not in the application namespace. The report status points to generated `RecordSet` refs.

The generated `RecordSet` name is stable and short:

```text
gwrs-<hash>-<record-type>
```

`<hash>` is computed from the source route namespace, source route name, selected Zone namespace, selected Zone name, and hostname. The record type is not part of the hash input because it is present as the final suffix. This keeps records for the same route, Zone, and hostname visually grouped:

```text
gwrs-8f3a2c91d7-a
gwrs-8f3a2c91d7-aaaa
```

The exact hash length is an implementation detail, but it must be long enough to avoid practical collisions and short enough to keep generated names readable.

Generated `RecordSet` resources carry labels and annotations identifying the owning `HTTPRouteDNSReport` and source `HTTPRoute`. Short selector values and hashes go in labels. Potentially long values, such as route namespace, route name, hostname, and Zone reference, go in annotations because Kubernetes label values have a 63-character limit.

Generated `RecordSet` resources are not user-owned records. If a report no longer needs a generated `RecordSet`, the controller deletes it.

The controller projects generated `RecordSet.status.conditions` into `HTTPRouteDNSReport.status.hostnames[].recordSets[].generated.conditions[]` so users can see both source translation and provider programming state from the report.

## Deletion and Cleanup

`HTTPRouteDNSReport` is created in the same namespace and with the same name as the source `HTTPRoute`, so it can use an owner reference to the `HTTPRoute`. Generated `RecordSet` resources are created in `recordSetNamespace`, which may be a different namespace. Because Kubernetes owner references cannot safely express cross-namespace ownership, generated `RecordSet` resources are not garbage-collected by the report owner reference.

The Gateway DNS controller adds a finalizer to each `HTTPRouteDNSReport`. When the source `HTTPRoute` is deleted, Kubernetes marks the report for deletion through the owner reference. The controller then:

1. Finds generated `RecordSet` resources owned by the report using labels and annotations.
2. Deletes those generated `RecordSet` resources.
3. Waits until their deletion is accepted by the Kubernetes API.
4. Removes the report finalizer.

This cleanup path prevents generated DNS records from remaining after the source `HTTPRoute` is deleted.

The same ownership index is used for updates. When a report reconciliation no longer includes a previously generated `RecordSet` because a hostname, parent, Zone, target, or record type changed, the controller deletes the obsolete generated `RecordSet`. The controller must delete only records that carry its managed labels and report ownership annotations.

## Conditions

`HTTPRouteDNSReport.status.conditions` summarizes the route-level DNS state. DNS outcome details live in `status.hostnames[]`.

Each `status.hostnames[]` item represents one DNS hostname after Gateway route/listener hostname resolution:

- `hostname`: fully qualified DNS hostname.
- `zone`: selected Zone, candidate Zones, and conditions for this hostname's Zone decision. If no Zone is selected, this field records why selection failed.
- `recordSets`: desired DNS `RecordSet` objects for this hostname.

`zone.conditions[]` contains Gateway DNS controller conditions for the hostname's Zone decision. It does not copy `Zone.status.conditions`.

Each `recordSets[]` item represents one desired dns-api `RecordSet` candidate for the hostname:

- `type`: DNS record type the controller intends to create, such as `A` or `AAAA`.
- `name`: zone-relative `RecordSet.spec.name`.
- `ttl`: `RecordSet.spec.ttl`, when the controller sets one.
- `a`, `aaaa`, `cname`, or `options`: the DNS record body fields the controller intends to create.
- `conditions`: Gateway DNS controller conditions for this desired `RecordSet`, such as target resolution, policy allowance, and creation.
- `generated`: generated `RecordSet` reference and copied `RecordSet.status.conditions`, when a `RecordSet` was created.

The `recordSets[]` item mirrors the relevant `RecordSet.spec` fields for Gateway integration: `type`, `name`, `ttl`, `a`, `aaaa`, `cname`, and `options`. It does not repeat `zoneRef` or `provider`; the selected Zone is represented by `hostnames[].zone`, and the provider is derived from that Zone.

Initial `zone.conditions[]` condition types:

- `Selected`: whether the controller selected a dns-api `Zone` for the hostname.

Initial `recordSets[].conditions[]` condition types:

- `Resolved`: whether the controller resolved DNS hostnames, Gateway parents, targets, zones, and policy enough to create the desired `RecordSet` resources.
- `Allowed`: whether the generated `RecordSet` is allowed by dns-api `Zone.spec.allowedRecordSets` policy.
- `Created`: whether the generated `RecordSet` object was created.

Initial `Resolved=False` reasons include:

- `HostnameNotResolved`
- `ParentNotAccepted`
- `GatewayNotResolved`
- `GatewayTargetNotResolved`
- `MultipleGatewayTargetsUnsupported`
- `EndpointRecordSetRefineUnsupported`
- `EndpointRecordSetRefineUnavailable`
- `EndpointRecordSetRefineDenied`
- `EndpointRecordSetRefineInvalid`
- `EndpointRecordSetRefineFailed`
- `TTLNotResolved`
- `ZoneNotResolved`
- `RecordSetNotAllowed`
- `UnsupportedTarget`

Initial `Created=False` reasons include:

- `RecordSetNotCreated`

Generated `RecordSet.status.conditions` are copied under `status.hostnames[].recordSets[].generated.conditions[]`. They are not mixed into `status.hostnames[].recordSets[].conditions[]`, so future `RecordSet` condition types cannot collide with Gateway DNS controller condition types.

The report must not hide partial failures. If one desired `RecordSet` is ready and another desired `RecordSet` is blocked, the route-level condition summarizes the blocked state and `status.hostnames[].recordSets[]` identifies the exact hostname, selected or attempted zone, record type, intended record body fields, and reason.

## User Journey

Application user creates an `HTTPRoute`:

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
      sectionName: https-api
  rules:
    - backendRefs:
        - name: web
          port: 8080
```

The referenced Gateway listener declares the hostname:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  namespace: platform
  name: public
spec:
  listeners:
    - name: https-api
      protocol: HTTPS
      port: 443
      hostname: api.example.com
```

The controller creates:

```text
HTTPRouteDNSReport app/web
```

The report shows that the controller is trying to create an `A` record for `api.example.com`, which DNS record body it intends to write, which dns-api `Zone` matched, whether the generated `RecordSet` was allowed, and whether it is programmed.

## Alternatives Considered

### Create RecordSet Directly

The controller could create `RecordSet` resources directly from `HTTPRoute` without a report resource. This hides important failure modes. If no zone matches or `allowedRecordSets` rejects the generated record, there is no `RecordSet` object on which to place status. Users would need controller logs, similar to external-dns skip behavior.

### Controller-Owned Desired Resource With Spec

The controller could create an `HTTPRouteRecord` or `GatewayRecord` with `spec` containing desired hostnames and targets. This became awkward because some DNS hostnames come from `HTTPRoute.spec.hostnames`, some come from Gateway listener hostname, and targets come from `Gateway.status.addresses`. The boundary between desired state and observed translation was unclear.

`HTTPRouteDNSReport` avoids that ambiguity by presenting the integration output as a report.

### Report in Management Namespace

The report could live in `dns-api-system` together with generated `RecordSet` resources. That avoids granting report create/update permissions across application namespaces, but it makes the report harder for application teams to find and prevents a same-namespace owner reference to the source `HTTPRoute`.

The initial design places the report in the same namespace and name as the `HTTPRoute`.

### Provider Logic in Gateway Controller

The Gateway DNS controller could translate Gateway addresses directly into provider-specific `RecordSet` shapes. For example, it could recognize AWS load balancer hostnames, resolve Route 53 alias hosted zone IDs, and omit `ttl` for Route 53 `options.alias`.

This is rejected. It would make the Gateway DNS controller depend on provider-specific DNS rules and data tables. New providers would require Gateway controller changes, and existing providers would spread their record-shaping rules across multiple controllers. Provider-specific record shaping belongs to the Provider package and is exposed through the Endpoint RecordSet Refine API.

### Provider Webhook for Refine

The Provider package could expose Endpoint RecordSet Refine through a direct webhook. This is simpler to implement than an aggregated API server, but a controller-origin webhook call would need dns-api-specific authentication, authorization, TLS, retry, timeout, and audit conventions.

The initial design uses an aggregated API served through `APIService`. The Gateway DNS controller calls the Kubernetes API server with a normal create request, and Kubernetes handles API routing, authentication, authorization, and audit. The Provider package owns the APIService and backing aggregated API server.

## Future Work

- Add route namespace, route label, route annotation, Gateway namespace, Gateway name, and Gateway label filters.
- Support `TLSRoute`, `TCPRoute`, `UDPRoute`, and `GRPCRoute`.
- Add multi-record-set Endpoint RecordSet Refine for providers that need several `RecordSet` resources for one endpoint candidate.
- Add Route 53 routing policy support so multiple Gateway alias targets can be represented.
- Add UI views for `HTTPRouteDNSReport` in the Headlamp plugin.
- Add report aggregation in `Zone` or `RecordSet` views.
- Support user-authored source policies if report-only resources are not enough for future workflows.
