# DNS API UI Design

dns-api provides a React UI for managing DNS API resources on the Kubernetes API. The UI package is not tied to Headlamp; the same UI package can run in Headlamp, the mockup environment, and future platforms.

This document is the source of truth for the UI. Headlamp Desktop plugin registration, distribution, and Desktop-specific behavior are defined in [docs/design/headlamp-plugin.md](./headlamp-plugin.md). Visual layout and state checks are performed with the `mockup` package.

## Package Layout

The UI is managed as a Bun workspace. The root `package.json` and `bun.lock` are the workspace source of truth. The root `package.json` does not expose developer-facing scripts. Start, build, package, and check commands are exposed through Taskfile.

Package directories live under `packages/`. Because this repository is already the DNS API repository, package directory names do not repeat `dns-api`.

```text
package.json
bun.lock

packages/
  ui/
    package.json
    src/
      api/
      app/
      components/
      platform/
      resources.ts
      theme/
      types/
      views/

  headlamp-plugin/
    package.json
    src/
      index.tsx
      HeadlampPluginRoot.tsx
      platform.ts
      theme.ts
      routes.ts
    dist/

  mockup/
    package.json
    index.html
    src/
      main.tsx
      MockShell.tsx
      mockPlatform.ts
      mockData.ts
      mockTheme.ts
      styles.css
```

The `ui` package is the UI core. It must not import Headlamp APIs, `@kinvolk/headlamp-plugin`, or Headlamp components.

The `headlamp-plugin` package is Headlamp-specific. It contains only the `package.json` needed for Headlamp plugin recognition, route and sidebar registration, conversion from Headlamp APIs to the `ui` package `platform` interface, and conversion from Headlamp theme data to the DNS API UI theme. Because it is the Headlamp-specific package, do not create nested adapter directories such as `adapters/headlamp/`.

The `mockup` package is a development-only visual preview and interaction check package. It runs with a Vite dev server and does not produce build, package, or deployment artifacts. `mockup` imports the `ui` package and injects a mock platform and mock theme.

## UI Package Responsibilities

The `ui` package provides:

- `DnsApiApp`: root component for the UI.
- `theme`: semantic tokens, CSS variables, and `DnsThemeProvider` for the DNS API UI.
- `platform`: interfaces and React provider for operations delegated to the host platform.
- `api`: React hooks and create / update helpers around the platform API.
- `resources.ts`: DNS API resource descriptors, resource keys, namespace / name helpers, and condition helpers.
- `types`: DNS API resource types.
- `components`: Headlamp-independent reusable UI components.
- `views`: `Overview`, `Zones`, `Zone Classes`, `Provider Identities`, forms, and delete confirmation screens.

`views` may hold screen-level state, platform calls, and resource-specific form state. `components` are reusable display components and must not call Headlamp or Kubernetes APIs directly.

## Platform API

`platform` represents functionality that the UI delegates to its host platform. The term means that Headlamp, Backstage, mockup, standalone, or another platform can provide the implementation.

Screens and components in the `ui` package do not call the Kubernetes API, Headlamp API, browser clipboard, navigation, or toast APIs directly. They go through the `platform` API.

The initial platform API has these groups:

- `useDnsData`: returns list state for `ZoneClass`, `Zone`, `RecordSet`, `ZoneUnit`, `Provider`, provider identities such as `Route53Identity`, `Namespace`, and `Event`.
- `useAccess`: returns create, update, delete, get, and list permission checks equivalent to `SelfSubjectAccessReview`.
- `createZone` / `updateZone`: sends a `Zone` manifest to the Kubernetes API.
- `createRecordSet` / `updateRecordSet`: sends a `RecordSet` manifest to the Kubernetes API.
- `createRoute53Identity` / `updateRoute53Identity`: sends a `Route53Identity` manifest to the Kubernetes API.
- `createSecret`: sends a core `v1/Secret` manifest to the Kubernetes API for provider identity credential setup.
- `updateSecretData`: patches one key in a core `v1/Secret` for provider identity credential rotation.
- `createZoneClass` / `updateZoneClass`: sends a `ZoneClass` manifest to the Kubernetes API.
- `clipboard`: copy text to the clipboard.
- `navigation`: screen navigation and deep-link generation inside the DNS API UI.
- `externalLinks`: opens URLs outside the DNS API UI through the host platform. AWS Console links use this API; the `ui` package does not call external service APIs.
- `notifications`: success, failure, and warning notifications.
- `liveYaml`: opens live Kubernetes object YAML.

The platform API is stable for UI needs. It does not expose Headlamp API shapes directly to the `ui` package.

This list is the initial design split. During implementation, platform functions may be added if needed. If a listed function is unnecessary, it may be removed. Additions, removals, merges, and splits are allowed as long as the `ui` package remains independent of Headlamp APIs.

Example:

```ts
export type DnsApiPlatform = {
  useDnsData: () => DnsData;
  useAccess: (resource: DnsResourceDescriptor, verb: string, attrs?: Record<string, string | undefined>) => boolean;
  createZone: (manifest: Zone) => Promise<void>;
  updateZone: (manifest: Zone) => Promise<void>;
  createRecordSet: (manifest: RecordSet) => Promise<void>;
  updateRecordSet: (manifest: RecordSet) => Promise<void>;
  createRoute53Identity: (manifest: Route53Identity) => Promise<void>;
  updateRoute53Identity: (manifest: Route53Identity) => Promise<void>;
  createSecret: (manifest: Secret) => Promise<void>;
  updateSecretData: (namespace: string, name: string, key: string, value: string) => Promise<void>;
  createZoneClass: (manifest: ZoneClass) => Promise<void>;
  updateZoneClass: (manifest: ZoneClass) => Promise<void>;
  clipboard: PlatformClipboardAPI;
  navigation: PlatformNavigationAPI;
  externalLinks: PlatformExternalLinksAPI;
  notifications: PlatformNotificationsAPI;
  liveYaml: PlatformLiveYamlAPI;
};
```

The `headlamp-plugin` package implements `DnsApiPlatform` for Headlamp. The `mockup` package provides a mock implementation with fixed data, latency, errors, RBAC-denied state, and empty state.

## Theme

The `ui` package has its own theme system. Components use DNS API UI CSS variables such as `--dns-ui-*` or semantic tokens from `DnsThemeProvider`. Components do not reference Headlamp theme objects, MUI theme objects, Backstage theme objects, or other platform-specific theme objects directly.

Theme tokens include color, border, radius, shadow, spacing density, font family, and focus ring. Tokens are named by role, not by a specific platform palette.

Example:

```ts
export type DnsTheme = {
  color: {
    surface: string;
    surfaceMuted: string;
    border: string;
    text: string;
    textMuted: string;
    accent: string;
    danger: string;
    warning: string;
    success: string;
  };
  radius: {
    sm: string;
    md: string;
  };
};
```

When running in Headlamp, `app/headlamp-plugin/src/theme.ts` converts Headlamp theme data into `DnsTheme` and passes it to the `ui` package. In the mockup environment, `mockTheme.ts` provides preview themes such as light, dark, and dense.

## UI Component Policy

The `ui` package does not use Headlamp components. All components are provided as local components, and their appearance is defined with DNS API UI theme tokens.

For functionality that is costly to implement correctly, such as accessibility, focus management, keyboard operation, and popover positioning, unstyled / headless libraries may be used. Do not use styled UI kits.

Initial dependencies:

- React / React DOM: UI runtime.
- Radix Primitives: accessible primitives such as Dialog, DropdownMenu, Popover, Tooltip, Select, Checkbox, and Tabs. Do not use Radix Themes.
- TanStack Table: headless table logic such as sorting, column state, and pagination.
- React Hook Form: form state and validation integration for create and edit forms.
- Zod: validation at the form-value and manifest-builder boundaries.
- Lucide React: icons.
- clsx: className composition.
- highlight.js: YAML syntax highlight with only required languages imported.

Tailwind, MUI, Radix Themes, Headlamp components, and other styled UI kits are not dependencies of the `ui` package.

Even when using Radix, `views` do not use Radix components directly. They go through local wrappers such as `components/Dialog.tsx`, `components/Select.tsx`, and `components/Tooltip.tsx`.

## YAML Display

Manifest preview, label snippets, adoption external refs, and provider status are displayed with the `YamlCodeBlock` component.

`YamlCodeBlock` is read-only and has no editing functionality. Syntax highlight uses `highlight.js`. Do not use an external highlight CSS theme directly; adapt highlighting to DNS API UI theme tokens.

The copy button is an optional `YamlCodeBlock` action. Clipboard writes go through `platform.clipboard`. The current UI uses a compact button with a copy icon and `Copy` / `Copied` text.

Create and edit form `Manifest preview` always shows the best-effort Kubernetes manifest YAML that can be built from the current form state, even when required fields are empty or client-side validation has errors. Missing required fields, invalid field values, and incomplete type-specific branches are shown as validation messages near the preview and near the relevant form field. They must not replace the YAML with a single error-only code block such as `error: Namespace is required`.

When a preview contains more than one Kubernetes object, it is rendered as a YAML document stream with `---` document separators, not as a YAML sequence. The preview must not prefix each object with list item markers such as `- apiVersion`.

When a required field is empty, the preview omits that field instead of inserting a placeholder value. For example, if `metadata.namespace` is not selected yet, the partial preview still shows `apiVersion`, `kind`, `metadata.name` when available, and the known `spec` fields, but it omits `metadata.namespace`. If a branch field depends on an unselected option, such as `RecordSet.spec.type`, the preview shows the common fields that are already known and omits the type-specific body until the type is selected.

The preview state is labeled as complete or incomplete. `Incomplete` means the YAML is useful for inspection but is not yet submit-ready. The submit button remains disabled until required fields, branch-specific fields, and client-side validation pass. Admission webhook validation remains authoritative after submit.

If the form state contains a value that cannot be converted into a Kubernetes object shape, the preview keeps the last buildable partial manifest when available and shows the conversion error outside the code block. The code block remains reserved for YAML content, not diagnostic prose.

## Mockup Package

The `mockup` package runs with a Vite dev server. The developer entry point is in Taskfile.

```yaml
task dns-api-ui:mockup
```

This task starts the Vite dev server for the `mockup` package. It may use a fixed port, but port conflicts must be explicitly overrideable.

The `mockup` package has no build task. It is not part of CI or distribution artifacts. If needed, it is limited to TypeScript checks. It does not produce visual preview artifacts.

The mockup provides a top-level `State` selector with these states:

- `Data`: normal mock data.
- `Empty`: no `Zone`, `RecordSet`, `ZoneUnit`, or `Event`.
- `RBAC denied`: create / update / delete actions are denied.
- `Admission error`: create / update fails as an admission webhook error.
- `Warning condition`: warning conditions exist on `Zone` and Event data.
- `Provider pending`: provider change is pending.
- `Conflict`: `RecordSet` shows `Accepted=False`, reason `RecordSetConflict`; `ZoneUnit` diagnostics are shown only when visible.
- `Delete blocked`: delete confirmation shows a reference or policy block.

The mockup does not use Headlamp APIs. It is used before Headlamp integration to validate layout, theme, responsive behavior, copy actions, and form branching.

## Screen Structure

The primary navigation contains `Overview`, `Zones`, `Zone Classes`, and `Provider Identities`. `Records`, `Conflicts`, and `Diagnostics` are not top-level screens.

In Headlamp, the `headlamp-plugin` package adds a `DNS` menu to the Headlamp sidebar and maps this navigation to Headlamp routes. In mockup, `MockShell` displays the same navigation.

`Overview` is an aggregate screen where users first notice problems. It shows summary tiles for `Zones`, `RecordSets`, `Provider Identities`, and `ZoneClasses`, plus active DNS alerts. Bad conditions such as `Accepted=False`, `Programmed=False`, `Ready=False`, and `RecordSetConflict` appear in alerts, with details linked to the relevant resource detail screen.

`Zones` is where application engineers manage `Zone` and its `RecordSet` children. Creating, editing, and deleting `RecordSet` starts from the records section on the `Zone` detail screen. `RecordSet` detail exists as a deep-link target, but `Records` is not a primary navigation item.

`Zone Classes` and `Provider Identities` are platform settings screens for platform engineers managing provider identity resources and `ZoneClass`. They are top-level navigation items. Provider labels and provider picker cards do not use provider-specific colors; they use the same neutral color. Providers are identified by provider logo icon and provider name.

The UI label for provider identity resources is `Provider Identity` for one resource and `Provider Identities` for the list, navigation item, breadcrumb item, button text, empty state, and delete confirmation. Do not use `Integration` or `Integrations` as the user-facing name for `Route53Identity`, `CloudflareIdentity`, or future provider identity resources.

List screens follow Kubernetes resource list conventions and show `Age` instead of exact creation timestamps. Detail screens may show exact timestamps when useful.

## Breadcrumbs

Each screen shows breadcrumbs that point to parent screens. Breadcrumbs appear at the top of the screen above the page heading. Screens with no navigable ancestor do not show breadcrumbs. This includes top-level screens such as `Overview`, `Zones`, `Zone Classes`, and `Provider Identities`.

Breadcrumbs represent the logical resource hierarchy, not browser history. The same screen shows the same breadcrumbs regardless of navigation path.

Breadcrumbs do not include the current screen. The current screen name appears in the page heading. Ancestor items are links when a corresponding screen exists; navigation uses `platform.navigation`.

Primary navigation entries `Overview`, `Zones`, `Zone Classes`, and `Provider Identities` are independent roots. Do not create an artificial shared root.

Breadcrumbs by screen, listing only ancestors:

- `Overview`, `Zones` list: hidden.
- `Zone` create: `Zones`.
- `Zone` detail: `Zones`.
- `Zone` edit: `Zones` > `<zone name>`.
- `Zone` delete confirmation: `Zones` > `<zone name>`.
- `RecordSet` create: `Zones` > `<zone name>`.
- `RecordSet` detail: `Zones` > `<zone name>`.
- `RecordSet` edit: `Zones` > `<zone name>` > `<record name> (<type>)`.
- `RecordSet` delete confirmation: `Zones` > `<zone name>` > `<record name> (<type>)`.
- `Zone Classes` list: hidden because it has no navigable ancestor.
- `ZoneClass` create: `Zone Classes`.
- `ZoneClass` detail: `Zone Classes`.
- `ZoneClass` edit / delete confirmation: `Zone Classes` > `<zone class name>`.
- `Provider Identities` list: hidden because it has no navigable ancestor.
- Provider Identity create: `Provider Identities`.
- Provider Identity detail: `Provider Identities`.
- Provider Identity edit / delete confirmation: `Provider Identities` > `<provider identity name>`.

Screens with no navigable ancestor link do not show breadcrumbs. `Zone Classes` and `Provider Identities` list screens do not show breadcrumbs. Their section context is shown by the selected primary navigation item.

Resource breadcrumb labels use resource names. Namespaces are shown in the page heading and are not repeated in breadcrumbs, but link identity keeps both namespace and name. If a cross-namespace `RecordSet` links to a parent `Zone`, navigation goes to the parent `Zone`'s namespace.

When a child screen is opened directly by deep link, parent identity is resolved from route params and resource references such as `RecordSet.spec.zoneRef`. While parent labels are loading, breadcrumbs show a neutral placeholder. If a parent resource has been deleted or cannot be fetched due to RBAC, the item becomes plain text with the known identity name instead of causing an error.

Breadcrumb items do not perform `SelfSubjectAccessReview`. Permission checks are handled by the destination screen after navigation.

Breadcrumbs are provided as a shared `ui` package component. Individual features do not assemble them separately. Trails are centrally defined per screen, and parent items are derived from resource references. Long resource names are truncated and expose their full value through `title` or equivalent. On narrow screens, middle items may collapse, but the root and last ancestor remain visible.

For accessibility, breadcrumbs use a `nav` element with `aria-label="breadcrumb"`. Breadcrumbs do not include the current screen, so `aria-current` is not used. Separators are decorative and are not announced by screen readers.

## Page Header Component and Text

Every full-page screen uses the same header component and grammar: breadcrumbs, H1, lead text, and header actions.

The shared `ui` package owns the page header component. Screens pass data into that component; they do not assemble separate header layouts. The implementation must converge the existing page title paths into one shared header API so spacing, typography, mobile stacking, breadcrumbs, and right-side actions behave the same across all screens.

Header component inputs are:

- `breadcrumbs`: optional list of parent breadcrumb items.
- `title`: required H1 text.
- `description`: optional lead text.
- `actions`: optional ordered action list rendered on the right side of the header on desktop and below the title block on narrow screens.

The header component is responsible for responsive layout. On desktop, text is left-aligned and actions are right-aligned at the top of the title block. On narrow screens, actions wrap below the description and remain left-aligned. Header actions must not overlap, clip, or push the H1 into unreadable line lengths.

Breadcrumbs show parent location and navigation. They do not show the current screen. They do not describe the action currently being performed.

There is no small uppercase mode label in the page header. Do not show `DNS`, `Platform Settings`, `NEW ZONE`, `EDIT RECORDSET`, `ZONE DETAIL`, or similar text above the H1. The screen mode is expressed by the H1, lead text, and actions.

The H1 is the main object, step, or action name. List screens use the plural resource name. Detail screens use the target resource display name. Picker steps use `Select <dependency>`. Create and edit forms use `New <Kind>` and `Edit <Kind>`. Delete confirmation screens use `Delete <Kind>` or the target name when the surrounding confirmation panel already names the delete action clearly.

The lead text is a short sentence below the H1. It explains the immediate user task and includes namespace/name when that helps disambiguate the target. The lead text must not repeat the breadcrumb.

Header actions are screen-level navigation or resource commands. They are not form submit buttons unless the whole page is a simple confirmation screen with no inner form panel. Form save buttons remain in the form panel footer. Destructive confirmation buttons remain in the confirmation panel footer. The header action for create, edit, picker, and delete confirmation screens is `Cancel` with a close icon. Detail screens use `Edit` plus a resource actions menu when more than one secondary command exists. List screens use one primary creation action when the user can create the listed resource.

Header action order is stable:

- list pages: primary create action, then secondary utility actions if any.
- detail pages: primary workflow action, `Edit`, then resource actions menu.
- form and picker pages: `Cancel` only.
- delete confirmation pages: `Cancel` only in the header; destructive delete is inside the confirmation panel.
- missing-resource pages: a single parent navigation action only when a useful parent screen exists.

Header text by screen:

| Screen | Breadcrumbs | H1 | Header actions |
| --- | --- | --- | --- |
| Overview | hidden | `Overview` | none |
| Zones list | hidden | `Zones` | `New Zone` when allowed |
| ZoneClass list | hidden | `Zone Classes` | `New ZoneClass` when allowed |
| Provider Identities list | hidden | `Provider Identities` | `New Provider Identity` when allowed |
| Provider Identity provider picker | `Provider Identities` | `Select Provider` | `Cancel` |
| Provider Identity create settings | `Provider Identities` | `New Provider Identity` | `Cancel` |
| Provider Identity detail | `Provider Identities` | `<provider identity name>` | `Edit`, resource actions menu |
| Provider Identity edit | `Provider Identities` > `<provider identity name>` | `Edit Provider Identity` | `Cancel` |
| Provider Identity delete confirmation | `Provider Identities` > `<provider identity name>` | `Delete Provider Identity` | `Cancel` |
| ZoneClass provider identity picker | `Zone Classes` | `Select Provider Identity` | `Cancel` |
| ZoneClass create settings | `Zone Classes` | `New ZoneClass` | `Cancel` |
| ZoneClass detail | `Zone Classes` | `<zone class name>` | `Edit`, resource actions menu |
| ZoneClass edit | `Zone Classes` > `<zone class name>` | `Edit ZoneClass` | `Cancel` |
| ZoneClass delete confirmation | `Zone Classes` > `<zone class name>` | `Delete ZoneClass` | `Cancel` |
| Zone ZoneClass picker | `Zones` | `Select ZoneClass` | `Cancel` |
| Zone create settings | `Zones` | `New Zone` | `Cancel` |
| Zone detail | `Zones` | `<zone domain name>` | `Add RecordSet`, `Edit`, resource actions menu |
| Zone edit | `Zones` > `<zone domain name>` | `Edit Zone` | `Cancel` |
| Zone delete confirmation | `Zones` > `<zone domain name>` | `Delete Zone` | `Cancel` |
| RecordSet create | `Zones` > `<zone domain name>` | `New RecordSet` | `Cancel` |
| RecordSet detail | `Zones` > `<zone domain name>` | `<record FQDN>` | `Edit`, `Delete` or resource actions menu |
| RecordSet edit | `Zones` > `<zone domain name>` > `<record name> (<type>)` | `Edit RecordSet` | `Cancel` |
| RecordSet delete confirmation | `Zones` > `<zone domain name>` > `<record name> (<type>)` | `Delete RecordSet` | `Cancel` |

Resource display names in H1 use the same rules as detail summaries: `Zone` uses `spec.domainName`; `RecordSet` detail uses the FQDN with a trailing root dot only when the UI already displays DNS names in fully qualified form; `RecordSet` edit and delete breadcrumbs use `<record name> (<type>)`; `Provider Identity` and `ZoneClass` use `metadata.name`. Namespace is shown in lead text or detail metadata, not in H1.

Lead text examples:

- Provider Identity create settings: `Create a Cloudflare DNS Provider Identity.`
- Provider Identity detail: `<namespace>/<name>.`
- Provider Identity edit: `Update credential settings for <namespace>/<name>.`
- ZoneClass settings: `Configure zone policy for <namespace>/<name>.`
- Zone create settings: `Create a public DNS zone with <zoneclass namespace>/<zoneclass name>.`
- Zone detail: provider or policy summary such as `Cloudflare public DNS zone.`.
- Zone edit: `Edit DNS access settings for <namespace>/<name>.`
- Zone delete confirmation: `Review provider effects before deleting <namespace>/<name>.`
- RecordSet create: `Manage a RecordSet inside <zone domain name>.`
- RecordSet detail: `<namespace>/<name>.`
- RecordSet edit: `Manage a <type> RecordSet inside <zone domain name>.`
- RecordSet delete confirmation: `Delete <record FQDN> from this Zone.`

Do not encode the action twice inside the title block. Use one H1 and one lead sentence; do not add a second visual mode label above the H1. For example, use H1 `Edit Zone` with lead text `Update DNS access settings for <namespace>/<name>.`, not a separate `EDIT ZONE` label plus `Zone settings`.

Do not introduce feature-local copies of the page header. List pages, not-found pages, picker pages, form pages, detail pages, and delete confirmation pages all use the same shared header component. A feature may provide helper functions that compute header props for a route, but those helpers return data and do not render their own title layout.

## Resource Detail Pattern

`Zone`, `ZoneClass`, Provider Identity, and `RecordSet` detail screens use the same resource detail pattern: shared page header, vitals strip, tab navigation, shared condition display, shared Event list, and shared Manifest operations. Individual screens choose different tab names and fields, but they do not create separate detail-page layout systems.

Detail page header actions use the shared page header. The normal action order is primary workflow action, `Edit`, then resource actions menu. Destructive delete actions are placed in the resource actions menu, not as always-visible header buttons. The menu item label names the resource kind, such as `Delete Zone`, `Delete ZoneClass`, or `Delete Provider Identity`.

The vitals strip is a shared summary grid directly below the header. It starts with `Status`, then shows high-value identity and reference fields such as `Used by`, `Namespace`, `Name`, `Provider`, `Provider identity`, `ZoneClass`, hosted zone ID, account ID, or domain. Vitals fields are links when the referenced resource is visible and navigable. Vitals do not duplicate the same concept twice under different labels.

Detail tabs group repeatable work surfaces. `Status` tabs show conditions and Events for the target resource. They may also show a separate related-resource health breakdown when parent or ownership resources are important to operating the target resource, but they do not present related-resource health as the target resource's own conditions. `Manifest` tabs use the same live YAML viewer and the same `Copy` and `Edit Manifest` actions. Missing Event RBAC or YAML edit capability degrades inside the relevant tab instead of breaking the whole detail page.

Shared detail components include:

- `ResourceDetailHeader`: wraps the shared page header and resource actions menu.
- `ResourceVitals`: renders the vitals strip with consistent label, value, link, and status treatment.
- `ConditionsTable`: renders Kubernetes conditions with status, reason, observed generation, transition time, and message.
- `EventsTable`: renders target-resource Events only.
- `ManifestTab`: renders live YAML with `Copy` and `Edit Manifest`.

## Common Display and Interaction Policy

Parent navigation is handled by breadcrumbs. Screens do not have separate `Back to <parent>` buttons. Remove back buttons whose only purpose is to return to a parent resource or parent list, including `Back to Zone`, `Back to Zones`, `Back to RecordSet`, `Back to ZoneClass`, `Back to ZoneClasses`, `Back to Provider Identity`, and `Back to Provider Identities`. Screens without breadcrumbs also do not need parent back buttons.

Status is shown through conditions and events. Do not invent UI-only health states that conflict with Kubernetes status. Bad states use restrained but clear visual treatment, not large decorative warning cards.

Status labels in compact list, table, and dense metadata cells use one shared `StatusBadge` component. This component is used for condition status and reason labels such as `Accepted`, `Ready`, `Programmed`, `Healthy`, `Syncing`, and `Degraded`, and for Kubernetes Event `type` labels such as `Normal` and `Warning` when those values appear in compact cells. Individual screens, tables, cards, and event lists must not implement separate badge styles for compact badge use cases.

`StatusBadge` is a borderless pill. It uses a tinted background, status-colored icon, and status-colored text. It does not draw a contrasting outline around the pill. The accepted / ready / normal success style is the filled green pill style, not the green outlined pill style. Badges may vary by semantic tone, such as success, warning, danger, pending, neutral, or unknown, but the shape, padding, icon placement, typography, and borderless treatment remain consistent.

Resource error states use the `danger` tone consistently. UI labels such as `Not accepted`, `Not programmed`, `Degraded`, failed Kubernetes operations, admission errors, and Kubernetes Event `type=Warning` are red danger states. They must not be shown with yellow or amber warning styling on some screens and red styling on others. The `warning` tone is reserved only for non-error cautions that do not mean the resource failed acceptance, programming, credential readiness, or user action.

List table condition columns and compact Event table `type` cells use `StatusBadge` rather than hard-coded local markup. When a compact status value needs additional details, the badge stays short and the reason or message appears in adjacent text, tooltip, or detail content.

Detail-page status summaries are not `StatusBadge` targets. The Zone detail header Status, Status tab `Health breakdown`, Status tab condition rows, and Status tab event rows use inline dot or icon plus text treatment. These views have enough horizontal space and surrounding context to show status as readable detail content instead of pill badges. They still use the same semantic tones as `StatusBadge`, including the `danger` tone for failed acceptance, programming, credential readiness, failed user action, and Kubernetes Event `type=Warning`.

Kubernetes API `403` / `409`, admission webhook errors, condition reasons, and related events are displayed in the context of create, update, delete, and detail workflows. Do not use raw provider SDK errors as primary display text; prefer resource condition messages and events.

Create and edit forms are not made only of technical labels. Fields that require user decisions include short help text. Provider-specific fields get dedicated forms for initial Route 53 major fields. If a safe form cannot be generated from schema, use a JSON editor fallback. Forms show a Kubernetes manifest preview before submit and while the user is still filling required fields.

`Description` is stored in `metadata.annotations["dns.appthrust.io/description"]`. Kubernetes resource references such as `Namespace`, `ZoneClass`, provider identity, and referenced `Zone` are selected with selectors or card pickers whenever possible, not free text.

## Provider Identities

The `Provider Identities` screen manages provider identity resources. It handles provider identity kinds declared by installed `Provider` resources, such as `Route53Identity` and `CloudflareIdentity`.

The Provider Identities list columns are `Name`, `Namespace`, `Provider`, `Kind`, `Conditions`, `Age`, and `Used by`. The `Provider` column shows the provider badge on the first line and the account ID on the second line. For `Route53Identity`, the account ID is `spec.accountID` because it is desired identity and is available before live credential checks finish. For `CloudflareIdentity`, the account ID is `status.account.id` because the account is observed from the API token. If the account ID is not available yet, show a muted `Account ID pending` value instead of leaving the line blank. The account ID line is plain text, not another badge.

Provider Identity creation shows a provider picker first. Provider-backed cards are generated from installed `Provider` resources, not from hard-coded provider display text. The card title is `Provider.spec.display.name`; the description is `Provider.spec.display.description`. If `Provider.spec.display.logo.url` is set, the card shows that logo before the title with clear spacing between the logo and provider name. The same provider name is not repeated as a second badge or pill inside the card. Provider picker cards do not use provider-specific colors; providers are identified by the logo and display name. Provider picker cards have visible spacing between cards; adjacent cards must not appear visually attached.

A provider-backed card is selectable when the UI supports creating the identity kind declared by the selected Provider version `versions[].identity.resource`. Selecting the card opens the matching provider identity create form. A provider that is not backed by a `Provider` resource may be shown only as a planned item. Planned items are disabled, do not use `Provider.spec.display.*`, show `Coming soon`, and do not open a create form.

The provider picker close / cancel action uses a standard close icon such as lucide `X`, with the text label `Cancel` when a text label is shown. It does not use a provider identity, provider, or settings icon for canceling the picker.

After selecting Route 53, the form includes `Name`, `Description`, `AWS account ID`, `AWS region`, `Credentials`, and `Assume role chain`. After selecting Cloudflare, the new Provider Identity form includes `Name`, `Description`, `Access token Secret source`, `Secret name`, `Secret key`, and, when the user chooses `Create Secret`, a `Cloudflare API token` secret-value field. The source choices are `Create Secret` and `Use existing Secret`; `Create Secret` is the default for the new form. The edit form shows the referenced Secret name and key, plus a `Token update` choice with `Leave token unchanged` and `Update token`. `Leave token unchanged` is the default. When `Leave token unchanged` is selected, saving the form must not update the Secret.

When `Update token` is selected on the Cloudflare edit form, the UI shows an empty `Cloudflare API token` secret-value field. The UI must never read, show, prefill, copy, or include the existing Secret value in preview, logs, notifications, or generated display text. On save, the UI patches only the selected Secret key with the entered token value through `updateSecretData`, then updates the `CloudflareIdentity` manifest. If the Secret patch fails, the UI does not update the `CloudflareIdentity`. If the Secret patch succeeds and the `CloudflareIdentity` update fails, the Secret remains updated and the UI reports the Kubernetes error without rollback.

For `Create Secret`, the UI creates a core `v1/Secret` in the selected namespace before creating the `CloudflareIdentity`. The Secret uses `type: Opaque` and writes the token through `stringData[<Secret key>]`. The resulting `CloudflareIdentity.spec.accessToken.secretRef.name` and `key` point at that Secret. If Secret creation fails, the UI does not create the `CloudflareIdentity`. If Secret creation succeeds and `CloudflareIdentity` creation fails, the Secret remains; the UI reports the Kubernetes error and does not attempt rollback.

The Cloudflare new-form manifest preview shows both the Secret and the `CloudflareIdentity` as a YAML document stream when `Create Secret` is selected. The Cloudflare edit-form manifest preview shows both the Secret update target and the `CloudflareIdentity` as a YAML document stream when `Update token` is selected. Secret values are never shown in clear text in the preview, logs, notifications, or error text generated by the UI; the preview may show the selected `stringData` key with a redacted value. `Use existing Secret` and `Leave token unchanged` show only the `CloudflareIdentity` manifest preview. Cloudflare help text states that the referenced or newly created token must include `Account Settings:Read` for exactly one Cloudflare account, plus `Zone:Edit` and `DNS:Edit` for all zones from that same account. The UI does not ask for `accountID`; the controller observes it from `CloudflareIdentity.status.account`.

`AWS account ID` is compared with the account returned by controller STS `GetCallerIdentity`, preventing DNS changes to unintended AWS accounts. `Assume role chain` is edited as row-based role entries, not a primary raw JSON textarea.

`Credentials` offers only `Runtime credentials` at first. Runtime credentials mean the controller Pod AWS SDK default credential chain. The UI does not ask for access keys, secret keys, session tokens, or SSO profile names.

Provider Identity detail uses the shared resource detail pattern. The header action order is `Edit`, then the resource actions menu. It reuses `ResourceDetailHeader`, `ResourceVitals`, `ConditionsTable`, `EventsTable`, and `ManifestTab`; it does not implement provider-identity-specific condition, Event, header, or Manifest layouts.

Provider Identity detail tabs are `ZoneClasses`, `Status`, `Credentials`, and `Manifest`.

The `ZoneClasses` tab lists `ZoneClass` resources that reference the provider identity. The tab label and table toolbar show the count. Columns are `Name`, `Zone creation`, `Zone deletion`, `Description`, and `Status`. The `Provider` column is omitted because the provider is already known from the current Provider Identity. `Status` uses the same compact `StatusBadge` treatment as RecordSet table condition columns. `Add ZoneClass` is in the table toolbar and opens ZoneClass creation with the current Provider Identity preselected.

The `Status` tab shows the provider identity conditions and target-resource Events using the shared components. It does not mix in Events from referencing `Zone` or `ZoneClass` resources. Status summary and condition/event detail rows use the resource detail status treatment, not `StatusBadge`, unless the value appears in a compact table cell.

The `Credentials` tab shows provider-specific credential observation. If a provider identity resource has `status.lastCredentialCheckTime` and `status.nextCredentialCheckTime`, show them as `Last credential check` and `Next credential check` with both absolute and relative time. `Route53Identity` has both. If `Next credential check` is empty, show that no automatic retry is scheduled.

Delete confirmation shows that provider identities still referenced by `ZoneClass` cannot be deleted.

## Zone Classes

`ZoneClass` is a provider-backed DNS zone capability published as platform policy to application namespaces. The initial UI handles only Public DNS. It does not show private hosted zone or VPC selection.

Creation starts with selecting a provider identity. Provider identities are grouped by provider and shown as cards. The `ZoneClass` is created in the same namespace as the selected provider identity.

The `ZoneClass` form handles `Name`, `Description`, `provider.name`, `provider.version`, `controllerName`, `identityRef`, provider policy parameters such as `zoneCreationPolicy`, `zoneDeletionPolicy`, `sameNameZonePolicy`, and `allowedZones`. `provider` and `identityRef.name` are derived from the selected provider identity and Provider version identity resource declaration. `provider` is shown as read-only provider name and version fields. `controllerName` is selected from the provider controller instance for the selected provider and is required. `identityRef` is shown as the selected provider identity, not as a raw group / kind / namespace editor. Hosted zone type is not selectable.

`sameNameZonePolicy` is shown only when `zoneCreationPolicy=Create`. The UI label is `Duplicate hosted zone policy`. When `zoneCreationPolicy=Deny`, this policy is hidden.

`zoneDeletionPolicy` uses labels `Retain hosted zone` and `Delete hosted zone`. The default visible choice is `Retain hosted zone`. If `Delete hosted zone` is selected, the form shows that deleting a `Zone` may delete the provider-side hosted zone.

`allowedZones` is edited as a namespace selector. The input UI uses a YAML mapping of `key: value` label pairs, not direct namespace names and not `key=value` rows. When editing a `ZoneClass` to narrow `allowedZones`, the UI previews whether existing `Zone` resources would become disallowed.

Route 53 `ZoneClass.spec.parameters.tags` is edited as a YAML mapping of `key: value` tag pairs. The UI does not show this as JSON. Invalid YAML or non-scalar tag values are shown as field-level validation errors below the tags input.

`ZoneClass` detail follows the shared resource detail pattern. The header actions are `Edit` and the resource actions menu. `Delete ZoneClass` is inside the resource actions menu and opens the delete confirmation screen; it is not a standalone header button.

The `ZoneClass` detail vitals strip shows `Status`, `Used by`, `Namespace`, `Name`, `Provider`, and `Provider identity`. `Status` is derived from `ZoneClass` conditions. `Used by` is the count of `Zone` resources referencing the `ZoneClass`. `Provider identity` links to the same-namespace provider identity detail page when the referenced identity is visible; otherwise it shows the known identity name as plain text. The vitals strip is the canonical place for these fields, so the body does not repeat `Provider` and `Provider Identity` in a separate basic information grid.

`ZoneClass` detail tabs are ordered `Zones`, `Status`, `Policy`, and `Manifest`.

`Zones` tab lists `Zone` resources that reference the `ZoneClass`. It reuses the same `Zone` table component as the `Zones` list page so columns, status display, empty states, sorting, and row links remain consistent. The tab toolbar shows the referencing Zone count and an `Add Zone` action when the user can create a `Zone` from this `ZoneClass`. `Add Zone` opens Zone creation with the selected `ZoneClass` fixed.

`Status` tab shows a health breakdown for the `ZoneClass` and referenced provider identity only. It does not include downstream health for referencing `Zone` resources; those are visible in the `Zones` tab and on each `Zone` detail page. The tab also shows `ZoneClass` conditions and Events whose `involvedObject` is the target `ZoneClass`.

`Policy` tab explains the published zone policy. It shows allowed zone namespace selectors, provider parameters, and policy values. `Zone creation policy`, `Duplicate hosted zone policy`, and `Zone deletion policy` show both the stored value and a short meaning for that value. The policy tab explains what the `ZoneClass` allows future `Zone` resources to do; it does not show live provider status for individual `Zone` resources.

`Manifest` tab uses the same live YAML viewer as `Zone` and Provider Identity detail. It provides `Copy` and `Edit Manifest` actions when those operations are available.

The `ZoneClass` delete confirmation screen shows that a `ZoneClass` with referencing `Zone` resources cannot be deleted.

## Zones

`Zone` creation starts with selecting a `ZoneClass`. Only `ZoneClass` resources that the user can reference and that allow the target namespace are candidates. After selecting a `ZoneClass`, the user enters `Namespace`, `Name`, `Description`, `Domain name`, record access permission, and adoption. `provider` is a read-only field derived from `ZoneClass.spec.provider` and stored in the created manifest.

`Domain name` uses lowercase, no trailing root dot, and Punycode ASCII representation as input guidance. The authoritative validation is still the admission webhook.

Record access permission maps to `Zone.spec.allowedRecordSets`. The initial form has two modes: `Only this namespace` and `Selected namespaces and records`. In the selected mode, users edit namespace labels, record name patterns, and record types together. Namespace labels are edited as a YAML mapping of `key: value` label pairs, not as `key=value` rows. Invalid YAML, non-mapping values, and non-scalar label values are shown as field-level validation errors below the namespace labels input. The form must not provide namespace-only access without record restrictions.

Adoption manages an existing provider zone under dns-api. It is omitted for new creation. For Route 53, the UI provides a structured `Hosted zone ID` field and explains that the target must be a public hosted zone.

`Zone` detail is ordered for user work: health checks, RecordSet editing, and troubleshooting. It does not use a Crossplane-style `spec` / `status` order. Layout and information structure are checked in the live mockup package. Old static screenshots are not a source of truth.

### Zone Detail Screen

The top-level action order is `Add RecordSet`, `Edit`, then the resource actions menu. `Add RecordSet` is the primary action and opens `RecordSet` creation with the selected `Zone` fixed. `Edit` opens the `Zone` edit screen. The resource actions menu contains only `Reconcile now` and `Delete Zone`. `Delete Zone` is a red destructive item and opens delete confirmation. `Reconcile now` patches `Zone.metadata.annotations["dns.appthrust.io/reconcile-request"]`. The UI does not call provider APIs directly and does not update status directly. The annotation value set by the UI is an RFC 3339 UTC timestamp; it may include sub-second precision so consecutive clicks produce different values. Controllers do not interpret this value as a timestamp.

`Reconcile now` is enabled only when the user can patch the target `Zone` metadata annotation. After a successful patch, the UI shows `Reconcile requested` and does not mark health as successful immediately. If the patch fails due to RBAC or resourceVersion conflict, display it as a Kubernetes API operation failure, not a provider error. The UI may show `dns.appthrust.io/reconcile-request` in the live manifest, but it does not show it as a normal field in create or edit forms.

The primary `Zone` detail summary contains:

- `Domain`: `Zone.spec.domainName`, exact canonical value.
- `Namespace`: namespace badge.
- `Provider`: provider display name, adoption badge, provider console link, and provider zone ID. For Route 53, the link label is `Open in Route 53` and the ID label is `Hosted zone ID`. The Route 53 hosted zone ID prefers `Zone.status.provider.data.hostedZoneID`; if missing, use `Zone.spec.adoption.hostedZoneId`; if both are missing, disable `Open in Route 53`. For Cloudflare, the link label is `Open in Cloudflare` and the ID label is `Zone ID`. The Cloudflare Zone ID prefers `Zone.status.provider.data.zone.id`; if missing, use `Zone.spec.adoption.zoneID`; if both are missing, show `Zone ID: -` and disable `Open in Cloudflare`.
- `Zone Class`: link to referenced `ZoneClass`. If it cannot be fetched, show known namespace/name as plain text.

Provider console links are assembled from observed status and opened through `platform.externalLinks`; the `ui` package does not call provider APIs. The Cloudflare DNS records page URL is `https://dash.cloudflare.com/<account-id>/<zone-domain-name>/dns/records`, where `<account-id>` comes from the referenced `CloudflareIdentity.status.account.id` and `<zone-domain-name>` comes from `Zone.spec.domainName`. If the Cloudflare account ID cannot be read because the identity is missing, not ready, or hidden by RBAC, disable `Open in Cloudflare` and keep the Cloudflare Zone ID visible when available. Route 53 console URL assembly uses the Route 53 hosted zone ID and continues to use `platform.externalLinks`.

`Status` summary shows `Accepted`, `Programmed`, and `Name servers`. Conditions are displayed with type, status, reason, last transition time, observed generation, and message. If status is false or unknown, the reason is the main label and the message is visible without opening YAML.

Health computation:

- `Zone` is healthy when `Accepted=True` and `Programmed=True`.
- `ZoneClass` is healthy when `Accepted=True`.
- Provider identity is healthy when `Accepted=True` and `Ready=True` for Route 53.
- `RecordSets` are healthy when all child `RecordSet` resources are `Accepted=True` and `Programmed=True`.
- If any parent or child is false or unknown, the screen shows `Degraded` or `Syncing` according to the condition status and reason.

`Health breakdown` has rows for `Zone`, `RecordSets`, `ZoneClass`, and provider identity. For Route 53, provider identity is `Route53Identity`. Each row has one of `Healthy`, `Syncing`, or `Degraded`, a short reason, and a `View` link to the problematic resource. The `RecordSets` row shows healthy / syncing / degraded counts and links to the `RecordSets` tab.

Tabs are ordered `RecordSets`, `Status`, `Access`, `Name servers`, `Manifest`.

`RecordSets` tab lists child `RecordSet` resources that reference the `Zone`. It supports create, detail, edit, and delete flows. Columns are `Name`, `Type`, `Values`, `TTL`, `Accepted`, `Programmed`, and `Age`. `Values` is summarized and has an overflow tooltip when long.

`Status` tab displays condition tables, provider data YAML, optional `ZoneUnit` provider state, and recent events. `Provider data` shows `Zone.status.provider.data` directly in `YamlCodeBlock`. If the user can read the matching `ZoneUnit`, `Provider state` may show `ZoneUnit.status.provider.state` for SRE diagnostics such as pending provider change IDs. Claim `status.provider.data` is not over-summarized; SREs must be able to read hosted zone ID and similar public provider fields.

`Access` tab displays same-namespace access and `allowedRecordSets`. For cross-namespace rules, show namespace selector labels, record name pattern, and record types together. Do not show namespace access without record restrictions.

`Name servers` tab displays `Zone.status.nameServers`. If empty, show a pending or unavailable state based on conditions.

`Events` shows only Kubernetes Events whose `involvedObject` is the target `Zone`, ordered newest first. It does not mix in events from referenced `RecordSet`, `ZoneClass`, or `Route53Identity` resources. If Event RBAC is missing, the rest of the `Zone` display continues and this section shows a permission warning.

`Manifest` tab displays the live `Zone` object YAML read-only. It has `Copy` and `Edit Manifest` actions. `Edit Manifest` uses the host platform live YAML editor link; the `ui` package does not implement its own Kubernetes YAML editor. The Manifest tab shows `spec.adoption` and `status.provider.data` as separate fields and does not confuse adoption input with provider observations. Create/edit form `Manifest preview` is the desired manifest to apply and is separate from live manifest display.

The `Zone` delete UI is a confirmation screen opened from `Delete Zone` in the resource actions menu. It shows the target resource, domain, referenced `ZoneClass`, provider, hosted zone ID, `zoneDeletionPolicy`, remaining `RecordSet` count, and whether the provider-side zone will be deleted or retained. If any `RecordSet` still references the `Zone`, the delete button is disabled and the UI tells the user to delete or move the `RecordSet` resources first.

## Record Sets

`RecordSet` is created from the `Zone` detail screen. The selected `Zone` is fixed and cannot be reselected inside the form. `provider` is read-only, derived from the selected `Zone`, and stored in the created manifest.

The create form first asks for record type, then switches fields based on type. Initially it shows only types allowed by the selected `Zone.spec.provider` Provider version `recordSet.supportedTypes` and provider schema validation. The initial Route 53 UI supports standard A, standard AAAA, standard TXT, standard CNAME, standard MX, standard CAA, delegated NS, and Route 53 ALIAS. The initial Cloudflare UI supports standard A, standard AAAA, standard TXT, standard CNAME, standard MX except Null MX, standard CAA, and delegated NS. `type=A` accepts IPv4 addresses in a multiline textarea, one address per line. `type=AAAA` accepts IPv6 addresses in a multiline textarea, one address per line. `type=TXT` accepts TXT values in a multiline textarea, one logical value per line. `type=CNAME` accepts one target DNS name. `type=MX` accepts mail exchange rows with preference and mail server. `type=CAA` accepts certificate authority authorization rows with flags, tag, and value. `type=NS` accepts delegated name servers in a multiline textarea, one name server per line. When Route 53 ALIAS is enabled, address fields, CNAME target, MX records, CAA records, delegated name servers, TXT values, and TTL are not specified.

Single-column list inputs for `spec.a.addresses`, `spec.aaaa.addresses`, `spec.txt.values`, and `spec.ns.nameServers` use textarea controls instead of add / remove row controls. Empty lines are ignored before manifest preview and submit. The UI trims surrounding whitespace from each line and preserves the remaining user-entered value in the manifest preview and saved spec. The form warns on exact duplicate lines before submit, and the admission webhook remains the authoritative validation.

IPv6 address input preserves the string entered by the user in manifest preview and saved spec. The UI does not automatically convert it to canonical text. Client-side validation may show whether values parse as IPv6 and whether semantic duplicates exist. The admission webhook remains the authoritative validation.

TXT value input treats each non-empty line as one logical TXT value. The UI does not ask users to add Route 53 quotes or 255-octet chunks. Manifest preview shows the user-entered logical values. Route 53 quoting and chunking are controller concerns.

CNAME target input uses label `CNAME target` for `spec.cname.target`. Help text says the value is a DNS name, not an IP address, URL, URI, or Route 53 ALIAS target. It is entered as lowercase ASCII without a trailing dot. The UI may validate Punycode, underscore-prefixed verification labels, and URL-like mistakes before submit, but the admission webhook remains authoritative. The form does not allow `Name` value `@` for CNAME. The CNAME form shows the same TTL input as other standard records and does not show Route 53 ALIAS fields.

MX input is row-based. Each row has `Preference` for `spec.mx.records[].preference` and `Mail server` for `spec.mx.records[].exchange`. `Preference` is an integer from `0` to `65535`; lower values have higher delivery priority. `Mail server` is a DNS name, not an IP address, URL, URI, or Route 53 ALIAS target. It is entered as lowercase ASCII without a trailing dot. The form allows multiple rows, allows the same preference with different mail servers, and warns on exact duplicate preference / mail server pairs before submit. The admission webhook remains authoritative.

MX help text states that apex MX records are allowed, so `Name` may be `@`. It also states that dns-api does not verify whether the mail server name resolves or has A / AAAA records. The mail server may be outside the selected `Zone`.

The MX form supports Null MX only when the selected Provider version allows it. For Route 53, when the user selects `No inbound mail`, the form produces exactly one row with `preference: 0` and `exchange: "."`, disables adding other MX rows, and explains that the owner name declares it does not accept email. Switching back to standard MX removes the Null MX row and restores editable mail server rows. For Cloudflare `v1alpha1`, the UI does not show `No inbound mail` because the Cloudflare Provider validation rejects Null MX until Cloudflare API support is verified.

CAA input is row-based. Each row has `Flags` for `spec.caa.records[].flags`, `Tag` for `spec.caa.records[].tag`, and `Value` for `spec.caa.records[].value`. `Flags` is an integer from `0` to `255`. The UI should make `0` easy to select and may also expose `128` for issuer critical behavior, but it must not restrict flags to those two values. `Tag` is a lowercase ASCII alphanumeric value. The UI offers common tags `issue`, `issuewild`, and `iodef`, plus a custom tag input that accepts any value matching the CAA tag grammar. `Value` is a non-empty string. The form allows multiple rows and warns on exact duplicate flags / tag / value tuples before submit. The admission webhook remains authoritative.

CAA help text states that CAA controls which certificate authorities may issue certificates for the owner name. It also states that dns-api validates the CAA record shape but does not verify CA account parameters, CA domain ownership, `iodef` delivery, or whether a CA honors the policy. Apex CAA records are allowed, so `Name` may be `@`.

Delegated NS input uses a multiline textarea labeled `Name servers` for `spec.ns.nameServers[]`. Each non-empty line is one delegated name server. The value is a DNS name, not an IP address, URL, URI, or Route 53 ALIAS target. It is entered as lowercase ASCII without a trailing dot. The form warns on exact duplicate name servers before submit. The admission webhook remains authoritative.

Delegated NS help text states that this record delegates a child DNS name to another name server set. It is not the current `Zone`'s assigned name server list. The form does not allow `Name` value `@` or wildcard names for `type=NS`. It also states that dns-api does not verify whether the delegated name servers are authoritative, reachable, have glue records, or are managed by dns-api.

Provider options are displayed based on the Provider version referenced by `provider`. A provider-specific options section is shown only when the selected record type has visible provider options. If no provider option is available for the selected record type, the section is omitted rather than showing disabled controls.

For Route 53, A / AAAA alias targets are supported. Route 53 ALIAS controls are shown only for `type=A` and `type=AAAA`; they are not shown for `type=TXT`, `type=CNAME`, `type=MX`, `type=CAA`, or `type=NS`, including as disabled checkbox controls. When alias is used, standard addresses, CNAME target, MX records, CAA records, delegated name servers, TXT values, and TTL are omitted.

For Cloudflare, provider options are shown in a `Cloudflare options` section. `TTL mode` has `Fixed TTL` and `Automatic` choices. `Fixed TTL` writes core `spec.ttl` and omits `spec.options.ttl`. `Automatic` omits core `spec.ttl` and writes `spec.options.ttl: Auto`. `Proxy status` is a toggle shown only for A, AAAA, and CNAME. When `Proxy status` is enabled, `TTL mode` is forced to `Automatic`. `Comment` is a single-line text input. `Tags` is a row-based `Name` / `Value` editor that produces `spec.options.tags[]` strings in `name:value` form. Tags whose name starts with `cf-` are rejected before submit. The UI does not try to detect the Cloudflare account plan; if the API rejects tags because the selected account plan does not support them, the condition and event are shown as provider feedback.

For Route 53 ALIAS, the UI asks for the alias target DNS name, alias target hosted zone ID, and evaluate target health value. The hosted zone ID is the target resource hosted zone ID. For an Elastic Load Balancer target, this is the load balancer `CanonicalHostedZoneId`, not the dns-api `Zone` hosted zone ID. The UI does not call AWS APIs or Kubernetes workload APIs to discover or fill alias target values.

Route 53 ALIAS field labels are:

- `Alias target DNS name` for `spec.options.alias.dnsName`.
- `Alias target hosted zone ID` for `spec.options.alias.hostedZoneID`.
- `Evaluate target health` for `spec.options.alias.evaluateTargetHealth`.

The alias target hosted zone ID help text states that this is the hosted zone ID of the alias target resource. For Elastic Load Balancing, users provide the load balancer `CanonicalHostedZoneId`. It also states that this is not normally the dns-api `Zone` hosted zone ID, except when the alias target is another record in the same hosted zone. If the selected `Zone.status.provider.data.hostedZoneID` is available, the UI may show it as a copyable value labeled `Current Zone hosted zone ID` for same-hosted-zone alias use. It must not prefill `Alias target hosted zone ID` automatically.

When alias mode is enabled, the form hides or disables `TTL`, IPv4 address textarea, IPv6 address textarea, CNAME target, MX records, CAA records, delegated name server textarea, and TXT value textarea. The manifest preview for an alias record includes `spec.options.alias` and omits `spec.ttl`, `spec.a`, `spec.aaaa`, `spec.cname`, `spec.mx`, `spec.caa`, `spec.ns`, and `spec.txt`. Switching alias mode off restores the standard fields for the selected record type and removes `spec.options.alias` from the preview.

The manifest preview for a standard CNAME record includes `spec.type: CNAME`, `spec.name`, `spec.ttl`, and `spec.cname.target`. It does not include `spec.options.alias`. If the selected provider schema exposes provider-specific CNAME options later, those options are shown as provider-specific options and are not treated as Route 53 ALIAS.

The manifest preview for a standard MX record includes `spec.type: MX`, `spec.name`, `spec.ttl`, and `spec.mx.records`. It does not include `spec.options.alias`. The row order in the form may be preserved in the preview for readability, but provider comparison treats MX records as an unordered set.

The manifest preview for a standard CAA record includes `spec.type: CAA`, `spec.name`, `spec.ttl`, and `spec.caa.records`. It does not include `spec.options.alias`. The row order in the form may be preserved in the preview for readability, but provider comparison treats CAA records as an unordered set.

The manifest preview for a delegated NS record includes `spec.type: NS`, `spec.name`, `spec.ttl`, and `spec.ns.nameServers`. It does not include `spec.options.alias` and does not display `Zone.status.nameServers` as part of the desired manifest. The line order in the form may be preserved in the preview for readability, but provider comparison treats delegated name servers as an unordered set.

For Cloudflare standard records, the manifest preview includes `spec.options` only when at least one Cloudflare option is set. If `TTL mode=Automatic`, the preview includes `spec.options.ttl: Auto` and omits `spec.ttl`. If `Proxy status` is enabled, the preview includes `spec.options.proxied: true`; when it is disabled, the preview may omit `proxied` because the Cloudflare Provider schema defaults it to `false`. `comment` and `tags` are shown exactly as they will be saved in `spec.options`.

`RecordSet` detail uses the shared resource detail pattern. The H1 is the record FQDN and the lead text is `<namespace>/<name>`. The header action is `Edit` followed by the resource actions menu. `Delete RecordSet` is in the resource actions menu, not as an always-visible header button. `View YAML` is not a header action because live YAML is shown in the `Manifest` tab.

The `RecordSet` vitals strip shows `Status`, `Desired record`, `Record name`, `FQDN`, `Zone`, `Provider`, and `Ownership`. `Status` is computed from the target `RecordSet` `Accepted` and `Programmed` conditions plus observed generation. `Desired record` shows the record type and TTL mode or TTL value. `Ownership` shows whether this `RecordSet` is accepted as the owner claim, is waiting for missing composition input, or is rejected with a conflict such as `RecordSetConflict`. The ownership badge is content-width and left-aligned; it must not stretch across the panel.

`RecordSet` detail tabs are ordered `Record data`, `Status`, `Ownership`, and `Manifest`.

`Record data` groups only the desired record and provider-specific desired options from the `RecordSet` claim. It does not show data read from `ZoneUnit.status` or provider-controller internal state. The tab name is not `Answer` because it is desired state, not a live DNS query result. The first section is `Desired record`; it is derived from `RecordSet.spec`, not from a live DNS query. It shows owner name, record type, TTL or TTL mode, and the type-specific desired values:

- A / AAAA: addresses.
- TXT: logical TXT values.
- CNAME: target.
- MX: preference and mail server rows.
- CAA: flags, tag, and value rows.
- delegated NS: name server list.
- Route 53 ALIAS: alias target DNS name, alias target hosted zone ID, and evaluate target health.

Provider-specific desired options are shown in a separate `<Provider> options` section only when visible options exist. For Cloudflare, this includes TTL mode, proxy status, comment, and tags. Observed provider record details such as owner name, content, TTL, proxy state, comments, and tags are not shown in `Record data`; those details are provider-controller internal state. Public provider IDs from `RecordSet.status.provider.data`, such as Cloudflare `records[].id`, are shown in the `Status` tab as provider data YAML.

`Status` shows a `Related resources` panel, the target `RecordSet` conditions, target-resource Events, and provider data YAML. Provider data YAML is exactly `RecordSet.status.provider.data`; it does not read or merge `ZoneUnit.status.recordSets[].provider.state`. `Related resources` includes `Zone`, `ZoneClass`, Provider Identity, and the matching `ZoneUnit` when visible. The matching `ZoneUnit` may be linked as a related resource, but its internal provider state is not embedded in the `RecordSet` detail body because the `RecordSet` screen is scoped to the `RecordSet` claim. `Zone` health uses `Accepted` and `Programmed`; `ZoneClass` health uses `Accepted`; Provider Identity health uses `Accepted` and `Ready`; `ZoneUnit` health uses resource-level `Programmed`. Visible related resources have `View` links. If a related resource is not visible due to RBAC, loading, deletion, or missing data, the row shows the known reference name and a neutral pending state instead of failing the page. Related-resource health is separate from the target `RecordSet` conditions.

The Event list includes only Kubernetes Events whose `involvedObject` is the target `RecordSet`, ordered newest first. It shows `Type`, `Reason`, `Message`, `Last seen`, and `Count`. Common events and provider-specific events whose reasons start with names such as `Route53` are shown together because they belong to the same `RecordSet`. Events for the referenced `Zone`, `ZoneClass`, Provider Identity, or `ZoneUnit` are not mixed into the target `RecordSet` Event list. Empty event lists show an empty state. Missing Event RBAC does not block the `RecordSet` body; the Event section shows a permission warning.

`Ownership` explains provider-side deletion impact. It shows whether the target `RecordSet` is accepted as the owner claim, whether the provider-side record set will be deleted when this `RecordSet` is deleted, and the record identity. It may link to the matching `ZoneUnit` when visible, but it must not display `ZoneUnit.status.recordSets[].provider.state`. If the target `RecordSet` is not the owner claim, the screen shows `RecordSetConflict` and states that deleting the rejected `RecordSet` will not delete the provider-side record set.

`Manifest` displays the live `RecordSet` YAML read-only. It has `Copy` and `Edit Manifest` actions and uses the same live YAML viewer behavior as `Zone`, `ZoneClass`, and Provider Identity detail screens.

`RecordSet` edit uses the same structure as create but exposes only mutable fields. `zoneRef`, `provider`, `type`, `name`, resource namespace, and resource name are immutable; changing them requires creating a new `RecordSet`. `adoption` is editable. If it points to a different external record set after a record set is already managed, the controller stops with `Accepted=False`, reason `ManagedResourceMismatch`; the edit UI displays that condition.

`RecordSet` delete UI is a confirmation screen opened from `RecordSet` detail. It shows the target, FQDN, record type, current values, allocation ownership, and whether the provider-side record set will be deleted. If the target `RecordSet` is not the allocation owner, the UI displays `RecordSetConflict` and shows that the provider-side record set will not be deleted.

## Provider Handling

`Provider` is treated as capability display provided by provider packages. The initial UI does not create or edit `Provider` resources. Provider lists use `Provider.spec.display.name`, `Provider.spec.display.description`, optional `Provider.spec.display.logo.url`, and `spec.versions` to show available providers and versions. The logo is a URL; the UI does not expect binary data inside Kubernetes resources.

The UI knows the core resource structure and the main fields of the initial Route 53 provider. Provider-specific inline objects use the OpenAPI schema from the Provider version for forms and validation display. Provider schema, CEL validation messages, admission webhook errors, and condition reasons are shown in user-readable form.

Provider-specific OpenAPI schema `description` fields are the first source for generated or generic provider-specific help text. The UI uses them in form help, field tooltips, manifest preview explanations, and schema-driven documentation surfaces when a dedicated hand-written help string is not more specific. If a field is rendered by a dedicated form component, the dedicated text may be shorter or more contextual, but it must not contradict the Provider schema description. When a Provider schema field lacks `description`, the UI may show the label and validation shape but must not invent provider behavior beyond the schema and design.
