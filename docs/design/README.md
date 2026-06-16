# dns-api Design

`docs/design/` is the source of truth for the current dns-api design. Proposal history and temporary notes do not live here. Downstream implementation and user documentation should follow the committed files in this directory.

## Documents

- [Overview](./overview.md): project scope, initial provider scope, resource model, and condition scope.
- [Common Design](./common.md): roles, DEP process, namespace boundaries, descriptions, license, manual reconcile, and adoption.
- [Core API](./core-api.md): `ZoneClass`, `Zone`, `RecordSet`, `ZoneUnit`, statuses, conditions, and events.
- [Provider Extension](./provider-extension.md): `Provider` contract, schemas, validation, support levels, and provider data.
- [Controllers and Webhooks](./controllers-webhooks.md): controller responsibilities, modules, acceptance boundary, and admission webhook.
- [Cloudflare Provider](./cloudflare-provider.md): Cloudflare identity, schema, ZoneClass parameters, provider data, reconciliation, deletion, and record mapping.
- [Route 53 Provider](./route53-provider.md): Route 53 identity, provider settings, and record-set provider schema.
- [Gateway API Integration](./gateway-api-integration.md): `HTTPRoute` / `Gateway` DNS report, Zone selection, and generated `RecordSet` behavior.
- [UI](./ui.md): reusable DNS API UI package, screens, workflows, components, and mockup design.
- [Headlamp Desktop Plugin](./headlamp-plugin.md): Headlamp Desktop plugin package, distribution, and Desktop-specific behavior.
- [Development Environment](./development-environment.md): devbox, Taskfile, kind / Tilt, local credentials, and CI setup.
- [Test Policy](./test-policy.md): kest, envtest, conformance scope, and initial scenario requirements.
- [Open Decisions](./open-decisions.md): unresolved design decisions.

## Source Rules

- Current behavior is described in present tense in these files.
- One-off proposals, pseudocode, resolved feedback, and obsolete manuals are not source of truth.
- API changes that need history or alternatives belong in `deps/` as DEP documents, then accepted behavior is reflected back here.
