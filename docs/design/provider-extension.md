# Provider Extension

## Provider

`Provider` is a cluster-scoped resource. A provider package creates one `Provider` per provider. It defines provider-specific inline objects, support level, validation, and display metadata for `ZoneClass`, `Zone`, and `RecordSet`. Multiple compatible schema versions are stored in `spec.versions`.

`Provider` belongs to the Core layer described in `docs/design/overview.md`. It is the Core Provider discovery contract. App Provider capabilities, such as endpoint record set support for Gateway, Ingress, or Service Apps, live in App-specific API groups instead of on `Provider`.

`ZoneClass.spec.provider`, `Zone.spec.provider`, `RecordSet.spec.provider`, and `ZoneUnit.spec.provider` use an object with `name` and `version`. `provider.name` is `Provider.metadata.name`; `provider.version` is `Provider.spec.versions[].name`. For example, `name: route53.dns.appthrust.io` and `version: v1alpha1` references version `v1alpha1` in `Provider/route53.dns.appthrust.io`.

The core API resolves Provider and version from those two fields. It does not interpret the DNS-name-like structure of the provider name or the semantics of the version name. Incompatible schema changes add a new version under the same `Provider` instead of changing an existing version destructively.

A provider controller has a runtime provider binding made of `providerName` and `providerVersion`. The binding defaults to the controller package's standard Provider contract, such as `route53.dns.appthrust.io/v1alpha1` or `cloudflare.dns.appthrust.io/v1alpha1`, and can be overridden by controller startup configuration for forks and compatibility testing. The configured `Provider` resource must expose schemas compatible with the controller implementation. Controllers use `providerName` to decide whether the referenced `Provider` belongs to them. They must not reject a claim or `ZoneClass` only because `spec.provider.version` names a served non-storage Provider version; provider-owned inline payload is validated against the served version and converted to the Provider storage version before provider-specific typed logic reads it. A controller may reject the resource with `InvalidProvider` when the referenced Provider name is not its configured provider name or when the storage Provider contract is incompatible with that controller implementation.

The manager exposes startup flags and matching environment variables for the initial providers: `--route53-controller-name` / `ROUTE53_CONTROLLER_NAME`, `--route53-provider-name` / `ROUTE53_PROVIDER_NAME`, `--route53-provider-version` / `ROUTE53_PROVIDER_VERSION`, `--cloudflare-controller-name` / `CLOUDFLARE_CONTROLLER_NAME`, `--cloudflare-provider-name` / `CLOUDFLARE_PROVIDER_NAME`, and `--cloudflare-provider-version` / `CLOUDFLARE_PROVIDER_VERSION`.

`route53-controller-name` and `cloudflare-controller-name` configure the controller name compared with `ZoneClass.spec.controllerName`. Defaults are `route53.dns.appthrust.io/controller` and `cloudflare.dns.appthrust.io/controller`. Provider controllers must not hard-code controller name as their only supported runtime binding; forks and multi-instance deployments can use these settings to claim a different `ZoneClass.spec.controllerName` while still using the same Provider contract.

`Provider.spec` fields:

- `display.name`: provider display name. Required.
- `display.description`: short display description. Optional.
- `display.logo.url`: provider logo URL. Optional. Binary data is not embedded in `Provider`.
- `versions`: provider schema versions.
- `versions[].name`: version name referenced by `provider.version`.
- `versions[].served`: whether the version can be referenced on create or spec update.
- `versions[].storage`: whether the version is the provider storage version used in `ZoneUnit`.
- `versions[].deprecated`: whether the UI and validation messages should treat the version as deprecated.
- `versions[].identity.resource.group`: API group of the provider identity resource selected by `ZoneClass.spec.identityRef`.
- `versions[].identity.resource.kind`: kind of the provider identity resource selected by `ZoneClass.spec.identityRef`.
- `versions[].identity.resource.scope`: identity resource scope. Initial supported value: `Namespaced`.
- `versions[].zoneClass.schemas.parameters`: OpenAPI v3 schema for `ZoneClass.spec.parameters`.
- `versions[].zoneClass.validationRules`: CEL validation rules for `ZoneClass`.
- `versions[].zone.schemas.adoption`: OpenAPI v3 schema for `Zone.spec.adoption`.
- `versions[].zone.schemas.statusProviderData`: OpenAPI v3 schema for `Zone.status.provider.data`.
- `versions[].zone.additionalConditionReasons`: provider-specific condition reasons allowed on `Zone.status.conditions`.
- `versions[].zone.validationRules`: CEL validation rules for `Zone`.
- `versions[].recordSet.supportedTypes`: DNS record types this provider version can handle. In `v1alpha1`, each value must be one of `A`, `AAAA`, `TXT`, `CNAME`, `MX`, `CAA`, or `NS`.
- `versions[].recordSet.schemas.options`: OpenAPI v3 schema for `RecordSet.spec.options`.
- `versions[].recordSet.schemas.adoption`: OpenAPI v3 schema for `RecordSet.spec.adoption`.
- `versions[].recordSet.schemas.statusProviderData`: OpenAPI v3 schema for `RecordSet.status.provider.data`.
- `versions[].recordSet.additionalConditionReasons`: provider-specific condition reasons allowed on `RecordSet.status.conditions`.
- `versions[].recordSet.validationRules`: CEL validation rules for `RecordSet`.
- `versions[].recordSet.disableValidations`: declarations disabling named core validations conditionally.
- `versions[].zoneUnit.disableValidations`: declarations disabling named core ZoneUnit composition validations conditionally. Initial supported name is `forbid-cname-coexistence`.
- `versions[].zoneUnit.validationRules`: CEL validation rules for ZoneUnit composition. Rules are evaluated against same-owner-name `ZoneUnit.spec.recordSets[]` item pairs before the items are accepted into the desired state.
- `versions[].zoneClass.conversion.toStorage.cel`: optional KRO CEL expression that converts `{parameters}` from this version to the Provider storage version.
- `versions[].zone.conversion.toStorage.cel`: optional KRO CEL expression that converts `{adoption}` from this version to the Provider storage version.
- `versions[].recordSet.conversion.toStorage.cel`: optional KRO CEL expression that converts `{options, adoption}` from this version to the Provider storage version.
- `versions[].*.conversion.toStorage.webhook.clientConfig.service.namespace`: namespace of the in-cluster Service that receives Provider conversion reviews. Required when webhook conversion is used.
- `versions[].*.conversion.toStorage.webhook.clientConfig.service.name`: Service name. Required when webhook conversion is used.
- `versions[].*.conversion.toStorage.webhook.clientConfig.service.path`: HTTPS path. Default: `/convert`.
- `versions[].*.conversion.toStorage.webhook.clientConfig.service.port`: Service port. Default: `443`.
- `versions[].*.conversion.toStorage.webhook.clientConfig.caBundle`: PEM CA bundle for verifying the Service certificate. Required when webhook conversion is used.
- `versions[].*.conversion.toStorage.webhook.timeoutSeconds`: request timeout. Default: `10`; minimum `1`; maximum `30`.

The core validating webhook resolves the `Provider` and version referenced by `provider` fields and uses the identity resource declaration, schema, and CEL rules for the target resource kind. `served=false` versions cannot be referenced by new resources or spec updates. Existing resources may still use their version schema for status validation.

Provider validation rules are grouped by the resource or composition target they validate. They do not have a shared `scope` field.

- `zoneClass.validationRules[]` validates one `ZoneClass`. CEL has `self` as that `ZoneClass`.
- `zone.validationRules[]` validates one `Zone`. CEL has `self` as that `Zone`.
- `recordSet.validationRules[]` validates one `RecordSet`. CEL has `self` as that `RecordSet`.
- `recordSet.disableValidations[]` conditionally disables named core `RecordSet` checks. CEL has `self` as that `RecordSet`.
- `zoneUnit.disableValidations[]` and `zoneUnit.validationRules[]` validate same-owner-name `ZoneUnit.spec.recordSets[]` item pairs. CEL has `self` and `other` as the record-set items, plus read-only `zone`, `provider`, and `toVersion` context.

A Provider must not use the older proposal shape with one shared `validationRules[]` list and `scope: Object` / `scope: Composition`. Rule placement selects the target and the available CEL input.

Provider CEL is evaluated with the KRO CEL runtime pinned in `go.mod`. The initial pinned KRO version is `github.com/kubernetes-sigs/kro v0.9.2`. dns-api does not define a CEL dialect. Provider `validationRules[]`, `disableValidations[]`, and `conversion.toStorage.cel` use KRO's CEL base environment, including KRO custom libraries, cel-go extensions, Kubernetes apiserver CEL libraries, CEL-to-Go JSON conversion, and `omit()` sentinel cleanup semantics. A KRO version upgrade is a provider contract compatibility change and requires conversion and validation expression compatibility checks.

Provider conversion follows a CRD-like served/storage model. Admission validates provider payloads against the served version selected by the claim. Before writing `ZoneUnit.spec`, the Core ZoneUnit Controller converts `Zone` and `RecordSet` provider payloads to the single `storage=true` Provider version. Provider ZoneClass controllers convert `ZoneClass.spec.parameters` to the Provider storage version before reading provider-specific parameters. If the input version already is storage, conversion is a no-op after validation and defaulting. If a declarative CEL conversion exists, core evaluates it. If a Provider conversion webhook is declared, core calls it and validates the returned payload against the storage schema.

CEL conversion is object-in/object-out. A `RecordSet` conversion receives one object containing the provider-owned fields, for example `{options, adoption}`, and returns one object with the same top-level field set. It does not use separate field-by-field expressions because that would make ordering and intermediate state hard to reason about.

Provider conversion webhook is a conversion boundary, not a provider reconciliation boundary. It must not create, update, delete, or observe DNS provider resources, and it must not require provider account credentials. It only converts the Provider-owned inline payload selected by `target` from a served version to the Provider storage version.

Core calls a Provider conversion webhook through HTTPS `POST` to the declared in-cluster Service. The request body is:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: ProviderConversionReview
request:
  uid: 5f0f0b5e-0000-0000-0000-000000000001
  provider:
    name: route53.dns.appthrust.io
  target: RecordSet
  fromVersion: v1alpha1
  toVersion: v1beta1
  payload:
    options: {}
    adoption:
      enabled: true
  context:
    zone:
      namespace: app
      name: example-com
      domainName: example.com
    recordSet:
      namespace: app
      name: www-a
      recordName: www
      type: A
```

The response body is:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: ProviderConversionReview
response:
  uid: 5f0f0b5e-0000-0000-0000-000000000001
  result:
    status: Success
    reason: Converted
    message: converted to storage version
    retryable: false
  payload:
    options: {}
    adoption:
      enabled: true
```

`response.uid` must equal `request.uid`. `response.result.status` is `Success` or `Failure`. On `Success`, `response.payload` is required and must validate against the storage version schema for the target. On `Failure`, `response.payload` is ignored. A retryable failure, timeout, connection error, TLS error, or 5xx response maps to the affected claim `Accepted=Unknown`, reason `ProviderConversionUnavailable`. A non-retryable failure, malformed response, UID mismatch, unsupported response kind, or storage-schema-invalid output maps to `Accepted=False`, reason `ProviderConversionFailed`. Served-schema-invalid input maps to `Accepted=False`, reason `InvalidProviderPayload`.

Provider admission validates webhook client configuration at Provider create/update time. It rejects missing service fields, missing `caBundle`, invalid timeout values, and configurations that combine `cel` and `webhook` for the same `toStorage` conversion. Exactly one conversion strategy is allowed for a non-storage served version target.

Provider admission validates validation CEL expressions at Provider create/update time before any `ZoneClass`, `Zone`, or `RecordSet` uses them. Compilation alone is not enough. Every provider validation CEL expression must compile in the same target-resource environment used at runtime and must have static result type `bool`. Non-bool expressions such as `self.spec.type` are rejected on the `Provider` because they would otherwise fail later during target resource admission. This applies to:

- `versions[].zoneClass.validationRules[].rule`, with `self` as a `ZoneClass`.
- `versions[].zone.validationRules[].rule`, with `self` as a `Zone`.
- `versions[].recordSet.validationRules[].rule`, with `self` as a `RecordSet`.
- `versions[].recordSet.disableValidations[].when`, with `self` as a `RecordSet`.
- `versions[].zoneUnit.disableValidations[].when`, with `self` and `other` as `ZoneUnit.spec.recordSets[]` items, plus `zone`, `provider`, and `toVersion`.
- `versions[].zoneUnit.validationRules[].rule`, with `self` and `other` as `ZoneUnit.spec.recordSets[]` items, plus `zone`, `provider`, and `toVersion`.

Provider admission error messages identify the failing field path and state that the CEL expression must return `bool`. Provider controllers must not observe or reconcile a `Provider` whose CEL expressions fail this admission contract.

Provider admission validates conversion CEL expressions separately. A conversion CEL expression must compile in the Provider conversion environment, where `self` is the provider-owned payload object and the expression result must be an object that can be validated against the storage target schema after defaulting. Conversion CEL expressions do not return `bool`.

`versions[].identity.resource` is required for provider versions that use provider identity CRDs. The initial dns-api providers use identity CRDs, so Route 53 and Cloudflare both declare it. The declaration is discovery and contract metadata: the core API can validate the `identityRef.name` shape and UI can link to the correct identity kind, while the provider controller remains responsible for resolving the identity object, checking provider-specific readiness, and applying provider-specific credential rules.

Provider admission validates `versions[].recordSet.supportedTypes` independently of any `RecordSet`. It rejects unknown types and types that do not have a core `RecordSet.spec` body in the current API version. A provider package must not claim `SRV` or another future type until the core API adds the matching body field, validation rules, examples, and provider schema support. This keeps `RecordSet.spec.type`, the required body field, and Provider capability metadata consistent.

Provider admission validates `additionalConditionReasons` independently of any status update. Each entry has:

- `conditionType`: condition type the reason applies to, such as `Programmed`.
- `status`: condition status the reason applies to. Allowed values are `True`, `False`, and `Unknown`.
- `reason`: provider-specific reason string. It must be non-empty CamelCase and must not duplicate a core reason for the same condition type and status.
- `description`: human-readable explanation for documentation and UI. Required.

Provider-specific condition reasons are still stored in the standard `metav1.Condition.reason` field. dns-api does not add `providerReason` or replace `metav1.Condition` with a custom condition object. The validating webhook builds the allowed condition reason set from core reasons plus the selected Provider version's `additionalConditionReasons`.

Core reason names stay small and reusable. Provider contract problems use existing core reasons instead of narrower proposal-only names:

- Unsupported provider contracts use `InvalidProvider`, not `UnsupportedProvider`.
- Provider-owned inline object schema or CEL failures use `InvalidProviderPayload`, not `InvalidProviderOptions`.
- Provider-side API rejection uses the `Programmed=False` provider API reasons such as `ProviderInvalidRequest`, `ProviderAccessDenied`, or `ProviderUnavailable`, not `ProviderRejected`.
- Resources that cannot be programmed because acceptance is not complete keep `Programmed=Unknown` with `Reconciling` or a more specific existing reason; dns-api does not use `NotAccepted`.

Provider OpenAPI schemas include `description` on provider-specific fields. These descriptions are user-facing metadata for documentation, generated form help text, manifest preview explanation, and generic schema viewers. Descriptions are not validation messages; validation failures use `validationRules[].message`, admission webhook messages, condition messages, or provider API error summaries. Field descriptions must be concise plain English, must not contain secrets or environment-specific values, and must remain accurate for every account or plan that can use the provider version. Provider packages update the descriptions when they add, rename, remove, or change the meaning of provider-specific schema fields.

Descriptions are required for properties in:

- `versions[].zoneClass.schemas.parameters.openAPIV3Schema.properties`.
- `versions[].zone.schemas.adoption.openAPIV3Schema.properties`.
- `versions[].zone.schemas.statusProviderData.openAPIV3Schema.properties`.
- `versions[].recordSet.schemas.options.openAPIV3Schema.properties`.
- `versions[].recordSet.schemas.adoption.openAPIV3Schema.properties`.
- `versions[].recordSet.schemas.statusProviderData.openAPIV3Schema.properties`.

Descriptions are also required for nested object properties inside those schemas. Object-level descriptions are required when the object is visible as a grouped field in UI or documentation.

Built-in Provider manifests set square SVG logo URLs so the UI can identify providers visually without hard-coded asset bundles:

- Route 53: `https://cdn.jsdelivr.net/gh/glincker/thesvg@main/public/icons/aws-amazon-route-53/default.svg`
- Cloudflare: `https://cdn.simpleicons.org/cloudflare`

Planned provider cards that are not backed by a `Provider` resource are not Provider manifests and do not need `spec.display.logo.url`. If a Google Cloud DNS Provider manifest is introduced later, use `https://cdn.simpleicons.org/googlecloud` unless the provider package chooses a more specific Cloud DNS service icon.

Only explicitly published core validations can be disabled. Initial disableable validations:

- `require-ttl`: require `spec.ttl` for standard record bodies.
- `require-a-addresses`: require `spec.a.addresses` for `type=A` standard body.
- `require-aaaa-addresses`: require `spec.aaaa.addresses` for `type=AAAA` standard body.
- `require-txt-values`: require `spec.txt.values` for `type=TXT` standard body.
- `forbid-cname-apex`: reject `type=CNAME` with `spec.name="@"`. This may be disabled only by provider versions that intentionally support apex CNAME behavior.

These validations cannot be disabled: `require-provider`, `require-zone-ref`, `require-type`, `require-name`, `require-cname-target`, `require-mx-records`, `require-caa-records`, `require-ns-name-servers`, record name grammar, lowercase canonical form, label length, wildcard placement, type-specific underscore allowance, delegated NS apex rejection, delegated NS wildcard rejection, CNAME target validation, type/body exclusivity, `supportedTypes` consistency, namespace / record access policy, and `ZoneUnit` ownership validation.

### Controller Responsibility

`Provider` is not reconciled by a controller. It is installed as a cluster-scoped resource by provider packages and used by the core validating webhook for provider-specific validation of `ZoneClass`, `Zone`, and `RecordSet`. It also publishes identity resource metadata used by UI and generic reference display.

### Provider Runtime Boundary

The `Provider` resource is an API contract and discovery object. It is not a Go runtime interface. The core controllers and webhook use it to resolve provider versions, schemas, CEL rules, supported record types, identity resource metadata, and display metadata. The core package does not define a shared Go `DNSProvider` interface for all providers.

Provider runtime interfaces are local to each provider controller package. A provider controller defines the smallest interface needed by its own reconciliation loop and tests. Route 53 and Cloudflare do not need matching interface shapes because their provider APIs expose different operation models:

- Route 53 uses a hosted zone and record-set change-batch model. Its runtime interface may combine hosted zone operations, change polling, tagging, record-set listing, and record-set batch changes.
- Cloudflare uses separate zone and DNS record APIs. Its runtime interfaces may separate zone operations from DNS record operations.

Provider factories are also provider-specific. They resolve provider identity resources, credentials, SDK clients, and provider-specific readiness requirements before returning the local runtime interface. The core API only requires that `ZoneClass.spec.identityRef.name` points to the identity kind declared by the selected Provider version; the provider controller owns runtime identity resolution.

Fake providers are test boundaries, not public extension points. Unit tests and envtest-style controller tests use fake implementations of the provider package interfaces to verify finalizers, conditions, events, status provider data, adoption checks, provider error mapping, and retry behavior without calling real cloud APIs. These fakes should stay close to the controller package unless repeated cross-provider test logic proves that a shared helper removes real duplication.

Common code may be shared for provider reference parsing, Provider version resolution, condition reason naming, status provider data encoding, and validation helpers. It must not force Route 53, Cloudflare, or future providers into a single CRUD-shaped DNS provider abstraction.

Example:

```yaml
apiVersion: dns.appthrust.io/v1alpha1
kind: Provider
metadata:
  name: route53.dns.appthrust.io
spec:
  display:
    name: Amazon Route 53
    description: Public DNS zones and record sets managed through Route 53.
    logo:
      url: https://cdn.jsdelivr.net/gh/glincker/thesvg@main/public/icons/aws-amazon-route-53/default.svg
  versions:
    - name: v1alpha1
      served: true
      storage: true
      deprecated: false
      identity:
        resource:
          group: route53.dns.appthrust.io
          kind: Route53Identity
          scope: Namespaced
      zoneClass:
        schemas:
          parameters:
            openAPIV3Schema:
              type: object
      zone:
        schemas:
          adoption:
            openAPIV3Schema:
              type: object
          statusProviderData:
            openAPIV3Schema:
              type: object
      recordSet:
        supportedTypes:
          - A
          - AAAA
          - TXT
          - CNAME
          - MX
          - CAA
          - NS
        schemas:
          options:
            openAPIV3Schema:
              type: object
          adoption:
            openAPIV3Schema:
              type: object
          statusProviderData:
            openAPIV3Schema:
              type: object
        disableValidations:
          - name: require-ttl
            when: "has(self.spec.options) && has(self.spec.options.alias)"
          - name: require-a-addresses
            when: "has(self.spec.options) && has(self.spec.options.alias)"
          - name: require-aaaa-addresses
            when: "has(self.spec.options) && has(self.spec.options.alias)"
        validationRules:
          - rule: "self.spec.type == 'A' || self.spec.type == 'AAAA' || self.spec.type == 'TXT' || self.spec.type == 'CNAME' || self.spec.type == 'MX' || self.spec.type == 'CAA' || self.spec.type == 'NS'"
            message: "route53 supports A, AAAA, TXT, CNAME, MX, CAA, and delegated NS records"
          - rule: "!(has(self.spec.options) && has(self.spec.options.alias)) || self.spec.type == 'A' || self.spec.type == 'AAAA'"
            message: "route53 alias is supported only for A and AAAA records"
          - rule: "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.ttl))"
            message: "ttl must not be specified when route53 alias is used"
          - rule: "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.a) && has(self.spec.a.addresses))"
            message: "a.addresses must not be specified when route53 alias is used"
          - rule: "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.aaaa) && has(self.spec.aaaa.addresses))"
            message: "aaaa.addresses must not be specified when route53 alias is used"
          - rule: "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.txt) && has(self.spec.txt.values))"
            message: "txt.values must not be specified when route53 alias is used"
          - rule: "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.cname) && has(self.spec.cname.target))"
            message: "cname.target must not be specified when route53 alias is used"
          - rule: "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.mx) && has(self.spec.mx.records))"
            message: "mx.records must not be specified when route53 alias is used"
          - rule: "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.caa) && has(self.spec.caa.records))"
            message: "caa.records must not be specified when route53 alias is used"
          - rule: "!(has(self.spec.options) && has(self.spec.options.alias) && has(self.spec.ns) && has(self.spec.ns.nameServers))"
            message: "ns.nameServers must not be specified when route53 alias is used"
```
