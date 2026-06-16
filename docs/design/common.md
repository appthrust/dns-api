# Common Design

## Users and Responsibilities

Platform teams manage `ZoneClass`, provider-specific identity, `Zone`, delegation boundaries, ownership, and deletion policies. Application teams declare public names and verification records as `RecordSet` resources. Provider implementers reconcile provider state and update status according to the common API, conditions, and `Provider` contract.

## DEP Process

dns-api uses DEP, DNS Enhancement Proposal, as the API change proposal process analogous to GEP in Gateway API. DEP files live under the root `deps/` directory. `docs/design/` is the source of truth for the current specification. `deps/` stores proposed changes, decisions, alternatives, and compatibility analysis.

Initial `deps/` layout:

```text
deps/
  README.md
  dep-template.md
  dep-0001-provider-resource.md
  dep-0002-standard-txt-record.md
```

Large DEPs may become directories, for example `deps/dep-0001-provider-resource/index.md`, when they need diagrams, sample manifests, or supporting material. Single Markdown files are the default format.

Changes that require a DEP:

- Adding, removing, or changing the meaning of core API resources, fields, enum values, or immutability rules.
- Adding or changing condition reasons, Event reasons, message contracts, or status ownership.
- Changing the `Provider` contract, provider versions, provider schema validation, or provider data contract.
- Adding standard record bodies for additional DNS types, such as future `SRV`, to the core API.
- Changing cross-namespace policy, ownership, allocation, adoption, or deletion policy semantics.
- Changing conformance, support levels, or provider implementer compatibility contracts.
- Breaking changes, migrations, or version transitions.

Changes that do not require a DEP:

- Typos, wording fixes, and link fixes.
- Reorganizing `docs/manual/` or improving explanations of existing behavior.
- UI layout, copy, and ordering changes that do not change API contracts.
- Bug fixes and provider controller internal improvements that follow the existing design.
- Internal retry, logging, and metrics changes for a specific provider such as Route 53 that do not change externally visible API contracts.

DEP status is one of `Draft`, `Accepted`, `Implemented`, `Rejected`, `Withdrawn`, or `Superseded`. `Draft` means under discussion. `Accepted` means adopted as the planned specification. `Implemented` means `docs/design/`, implementation, and required `docs/manual/` have been updated. `Rejected` means not adopted. `Withdrawn` means the proposer withdrew it. `Superseded` means another DEP replaced it.

An `Accepted` DEP is reflected into `docs/design/` as current-tense specification. A DEP that has not been reflected into `docs/design/` is future direction or decision history, not the source of truth for implementers or documentation writers. Implementers and documentation writers follow committed `docs/design/` snapshots.

To move to `Implemented`, the change must be reflected in `docs/design/`, implemented, and documented in `docs/manual/` when user-facing documentation is required. The initial process does not fix strict approver, owner, or reviewer roles. Each DEP contains at least `Status`, `Owner`, `Created`, `Summary`, `Motivation`, `Goals`, `Non-Goals`, `API`, `Behavior`, `Compatibility`, `Documentation`, and `Alternatives`.

## Documentation Responsibilities

Design authors update `docs/design/` and `deps/`. `docs/design/` is the current specification. `deps/` contains API change proposals and decision history. Design authors do not update user-facing READMEs or manuals directly.

The document writer updates user-facing documentation. Its scope is `docs/manual/`, the repository root `README.md`, Artifact Hub README files under `deploy/artifacthub/headlamp-plugin/**/README.md`, and the distribution README for the Headlamp plugin package. These documents follow committed `docs/design/` snapshots.

The root `README.md` is limited to project overview and navigation. It states what dns-api provides, the initial scope, short descriptions of major resources, entry points for the three user roles, Headlamp plugin information, license, and links to detailed documentation. It does not collect detailed operations or API reference.

`docs/manual/` contains role-based workflows and reference material. Initial layout:

```text
docs/manual/
  README.md
  platform-engineer.md
  application-engineer.md
  provider-implementer.md
  headlamp-plugin.md
  troubleshooting.md
  reference/
    resources.md
    conditions.md
    route53.md
```

`docs/manual/README.md` is a role-based table of contents. `platform-engineer.md` covers cluster installation, `Provider`, `Route53Identity`, `ZoneClass`, and namespace policy. `application-engineer.md` covers `Zone`, `RecordSet`, adoption, cross-namespace access, and interpreting conditions. `provider-implementer.md` covers `Provider` resources, schema versions, controller ownership, provider data, and conformance. `headlamp-plugin.md` covers Plugin Catalog installation, manual installation, and capabilities / non-capabilities. `troubleshooting.md` covers admission errors, condition reasons, Route 53 identity, IAM, `allowedRecordSets`, and Headlamp plugin troubleshooting.

Artifact Hub README is a short Plugin Catalog-facing description. It states that the plugin calls only the Kubernetes API, does not call AWS APIs, does not handle provider credentials in Headlamp Desktop, requires dns-api CRDs, the controller, and the Route 53 provider to already be installed, and links to `docs/manual/headlamp-plugin.md`.

The Headlamp plugin package README is limited to developer-facing basics and build / package entry points. User installation steps and troubleshooting live in `docs/manual/headlamp-plugin.md` and the Artifact Hub README. Do not duplicate detailed user instructions across all READMEs.

If the document writer finds missing or contradictory design information, or missing field, condition, error wording, migration, or troubleshooting information required for user documentation, it creates feedback in `docs/design-feedback/`. The document writer does not edit `docs/design/` or `deps/` directly.

## Namespace Boundaries

`ZoneClass` and provider-specific identity resources may live in platform namespaces. `Provider` is a cluster-scoped definition supplied by the provider package. `Zone` may live in application namespaces. `RecordSet` may live in record owner namespaces.

`ZoneClass.spec.allowedZones` controls which namespaces may use a class for `Zone` resources. If `Zone.spec.zoneClassRef.namespace` is omitted, it references a `ZoneClass` in the same namespace as the `Zone`.

Platform engineers decide whether hosted zones can be created and which cloud account or provider policy is used through `ZoneClass` and `ZoneClass.spec.allowedZones`. Application engineers cannot freely choose any `ZoneClass` even if they can create `Zone` in their namespace. They can use only classes published to that namespace by platform engineers.

`RecordSet` belongs to exactly one `Zone`. A `RecordSet` in the same namespace may reference a `Zone` in the same namespace without additional grants. Cross-namespace `RecordSet` usage is granted by the referenced `Zone.spec.allowedRecordSets`. `allowedRecordSets` does not merely grant namespaces; it grants both namespace selectors and record name / type ranges. To publish the same record to multiple zones, create one `RecordSet` per zone.

## Description

Short user-facing descriptions are stored in `metadata.annotations["dns.appthrust.io/description"]`. `spec` remains desired state for the DNS provider, so there is no dedicated description field. The initial UI shows and edits this annotation for `ZoneClass`, `Zone`, and provider identity create, list, and detail screens.

## License

The repository-wide license is `Apache-2.0`. The same license applies to controllers, CRDs, sample manifests, UI, Headlamp plugin, design documents, and user-facing documents. Documentation does not use a separate license.

The repository root `LICENSE` file is the license source of truth. The copyright holder is `Appthrust`, and the `LICENSE` file includes `Copyright 2026 Appthrust`.

Package metadata, Artifact Hub metadata, and release artifact license metadata match the root `LICENSE`. The Headlamp plugin package `package.json` and Artifact Hub `artifacthub-pkg.yml` show `Apache-2.0`.

Per-source-file license headers are not required initially. If required later, define formatter behavior, code generator behavior, and generated-file handling in a separate design change.

Automatic dependency license checks, allowed-license lists, and NOTICE generation are out of scope initially. If required before distribution, separately design the release artifact scope and third-party notice generation.

## Manual Reconcile Request

Manual reconcile requests for `Zone` are stored in `Zone.metadata.annotations["dns.appthrust.io/reconcile-request"]`. This annotation is an operation request that users or UI can update with `kubectl patch` or equivalent. It is not desired DNS provider state. The value is an opaque string; controllers do not interpret it as a time, order, deadline, or request ID. Users or UI set a value different from the previous value for each request so consecutive requests can be observed.

`dns.appthrust.io/reconcile-request` is defined only for `Zone`. In the initial Route 53 implementation, Zone reconciliation re-observes the hosted zone and child `RecordSet` resources, so a Zone annotation is sufficient. There is no initial RecordSet-specific manual reconcile annotation.

The Core ZoneUnit Controller observes `Zone` metadata updates and writes the reconcile request to the matching `ZoneUnit` so the provider controller can enqueue it. The annotation does not guarantee immediate execution or a provider API call. If the controller is already waiting for a pending change, it follows normal pending-change and serialization rules. The controller does not remove the annotation; the last requested opaque value remains. `Programmed=True` means provider state was re-observed and matches `spec`, not that a manual request was accepted.

Manual reconcile requests do not update status directly. The UI shows a notification such as `Reconcile requested` after a successful annotation patch. Reconcile completion is determined from `status.conditions` and `status.provider.data`. Admission webhook does not validate the annotation value format. The annotation is not a substitute for `spec` validation or provider policy.

## External Resource Adoption

`Zone` and `RecordSet` may specify `spec.adoption` to bring existing external resources under dns-api management. Without `adoption`, provider controllers create new external resources. With `adoption`, provider controllers validate the referenced or derived external resource and adopt it only when it matches the spec.

`adoption` is a provider-specific opaque object. The core API defines only that it is an object. The meaning of its keys and values is defined by `Provider`. `Zone.spec.adoption` is validated by the Provider version referenced by `Zone.spec.provider` using `zone.schemas.adoption`. `RecordSet.spec.adoption` is validated by the Provider version referenced by `RecordSet.spec.provider` using `recordSet.schemas.adoption`.

`spec.adoption` is mutable. Admission webhook does not reject updates as immutable. However, provider controllers guard against redirecting an already managed resource to a different external resource. If `spec.adoption` is set and status already records the managed external resource identity, and the adoption target would refer to a different external resource, the provider controller does not call the DNS provider API. It sets `Accepted=False`, reason `ManagedResourceMismatch`, and stops reconciliation. For Route 53 `Zone`, the recorded identity is `status.provider.data.hostedZoneID`. For Route 53 `RecordSet`, the external identity is derived from the hosted zone and accepted record-set desired state. This guard is not applied when `adoption` is absent or status has not yet recorded a managed external resource.

This guard protects managed external resources. A mistakenly adopted external resource cannot be moved to another external resource by editing `spec.adoption`. To move, delete and recreate the `Zone` or `RecordSet`. With `zoneDeletionPolicy=Retain`, deleting a `Zone` can keep the Route 53 hosted zone so it can be recreated without losing external DNS data.

Adoption intent is stored in spec, not annotations or status, so GitOps can restore it. Annotation-based adoption is not provided.

The core validating webhook rejects resources whose `adoption` payload does not match the `Provider` schema. Provider controllers perform provider-side existence checks and spec matching that schema cannot express. If the external resource does not exist, set `Programmed=False`, reason `ExternalResourceNotFound`. If it exists but does not match the spec, set `Programmed=False`, reason `ExternalResourceMismatch`.

If no adoption is specified and an external resource with the same external identity already exists, the provider controller does not adopt it automatically. It sets `Programmed=False`, reason `ProviderConflict`.
