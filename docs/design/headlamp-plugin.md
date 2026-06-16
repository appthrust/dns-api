# Headlamp Desktop Plugin

dns-api provides a plugin package for Headlamp Desktop. The Headlamp Desktop plugin is one platform implementation of the DNS API UI; it is not the source of truth for the UI itself. Screen structure, resource workflows, component policy, theme, and mockup behavior are defined in [docs/design/ui.md](./ui.md).

The initial implementation targets only the Headlamp Desktop app. Public distribution targets installation through the Headlamp Desktop Plugin Catalog, published as an Artifact Hub `Headlamp plugin` package that the Plugin Catalog can read. In-cluster Headlamp distribution, Web app authentication design, and controller installation automation from the plugin are out of scope.

The initial compatibility target is Headlamp Desktop `v0.42.x`. If the target Headlamp minor version changes, first review plugin API differences, then update `app/headlamp-plugin/package.json` and this design.

## Package

The Headlamp plugin package is stored in `app/headlamp-plugin/`. The package name is `headlamp-plugin-dns-api`. The directory name stays `headlamp-plugin` because this is the Headlamp-specific package.

`app/headlamp-plugin/` is a Headlamp-specific package. Do not create a nested adapter directory such as `adapters/headlamp/`. Headlamp-dependent code is concentrated in `src/index.tsx`, `src/HeadlampPluginRoot.tsx`, `src/platform.ts`, `src/theme.ts`, and `src/routes.ts`.

`app/headlamp-plugin/` depends on `pkg/ts/ui/`. The `pkg/ts/ui/` package must not import Headlamp APIs or `@kinvolk/headlamp-plugin`.

```text
packages/
  headlamp-plugin/
    package.json
    src/
      index.tsx
      HeadlampPluginRoot.tsx
      platform.ts
      theme.ts
      routes.ts
    dist/
```

`src/index.tsx` is the entry point loaded by Headlamp. It registers routes, sidebar entries, plugin settings, and Desktop app menu entries. It does not contain UI layout or resource workflow logic.

`HeadlampPluginRoot` starts the `DnsApiApp` from the `ui` package and injects the Headlamp platform implementation and the DNS API UI theme converted from the Headlamp theme.

Example:

```tsx
<DnsApiApp
  platform={createHeadlampPlatform()}
  theme={createHeadlampTheme()}
/>
```

## Headlamp Plugin Recognition

The directory distributed as a Headlamp plugin contains at least `package.json` and the built `main.js`. The `headlamp-plugin` package build output is placed in the Headlamp plugin directory with this shape:

```text
$HEADLAMP_PLUGINS_DIR/
  headlamp-plugin-dns-api/
    package.json
    main.js
```

`app/headlamp-plugin/package.json` contains Headlamp plugin metadata and the `@kinvolk/headlamp-plugin` dependency. Its `license` field is `Apache-2.0`, matching the repository license. The root `package.json` is the source of truth for the Bun workspace but does not expose developer-facing scripts. Headlamp plugin operations are exposed through Taskfile.

## Catalog Publishing

To make the dns-api Headlamp plugin installable from the Headlamp Plugin Catalog, publish it as an Artifact Hub `Headlamp plugin` package. The Plugin Catalog reads Artifact Hub package metadata, then downloads the plugin archive from the archive URL and verifies the checksum from metadata.

The plugin archive is published as a GitHub Releases asset. Headlamp expects plugin downloads to come from Git hosting providers, so GitHub Releases is the canonical archive distribution location for dns-api. Do not use a custom CDN, container registry, npm registry, or project website as the canonical archive source.

The release tag, plugin package version, Artifact Hub package version, and archive file name use the same semantic version. For release `v0.1.0`, create these assets:

```text
headlamp-plugin-dns-api-v0.1.0.tar.gz
headlamp-plugin-dns-api-v0.1.0.tar.gz.sha256
```

`headlamp-plugin-dns-api-v0.1.0.tar.gz` extracts to:

```text
headlamp-plugin-dns-api/
  LICENSE
  package.json
  main.js
```

The `LICENSE` included in the archive is identical to the repository root `LICENSE`. The Headlamp plugin archive must not carry a different license text.

`headlamp-plugin-dns-api-v0.1.0.tar.gz.sha256` is a helper artifact for the SHA-256 checksum used in the Artifact Hub annotation. Artifact Hub `headlamp/plugin/archive-checksum` uses the same value in `sha256:<hex>` form.

Artifact Hub metadata is stored under `deploy/artifacthub/headlamp-plugin/` in this repository. This is the Artifact Hub repository root for the Headlamp plugin. The URL registered in Artifact Hub points to that directory, for example `https://github.com/appthrust/dns-api/deploy/artifacthub/headlamp-plugin`. Do not use a URL containing `tree/<branch>`.

Metadata layout:

```text
artifacthub/
  headlamp-plugin/
    artifacthub-repo.yml
    headlamp-plugin-dns-api/
      README.md
      artifacthub-pkg.yml
```

`artifacthub-repo.yml` is used for the Artifact Hub repository owner and publisher verification. Initially it contains owner `name` and `email`. After registering the repository in Artifact Hub, add the `repositoryID` shown by Artifact Hub if Verified publisher setup is required.

`artifacthub-pkg.yml` is the plugin package metadata. It contains at least:

```yaml
version: 0.1.0
name: headlamp-plugin-dns-api
displayName: DNS API
createdAt: "2026-06-01T00:00:00Z"
logoURL: https://example.com/dns-api-logo.svg
description: Manage dns-api Zone, ZoneClass, RecordSet, and Route 53 resources from Headlamp.
license: Apache-2.0
homeURL: https://github.com/appthrust/dns-api
annotations:
  headlamp/plugin/archive-url: https://github.com/appthrust/dns-api/releases/download/v0.1.0/headlamp-plugin-dns-api-v0.1.0.tar.gz
  headlamp/plugin/archive-checksum: sha256:<checksum>
  headlamp/plugin/version-compat: ">=0.42.0 <0.43.0"
  headlamp/plugin/distro-compat: "app,mac,linux"
```

`license` is `Apache-2.0`, matching the repository root `LICENSE`. Artifact Hub metadata must not advertise a different license.

`headlamp/plugin/version-compat` matches the Headlamp Desktop minor version targeted by this design. While only Headlamp Desktop `v0.42.x` is verified, use `>=0.42.0 <0.43.0`. If supported Headlamp minor versions change, review plugin API differences and update `app/headlamp-plugin/package.json`, this design, and Artifact Hub metadata in the same release.

`headlamp/plugin/distro-compat` is initially `app,mac,linux`. Do not include `windows` until packaged-plugin smoke testing for Windows is standardized. Do not include `in-cluster` or `web` until in-cluster Headlamp distribution is in scope.

Catalog publishing release workflow:

1. Run `task headlamp-plugin:check`.
2. Run `task headlamp-plugin:package` to create the archive and SHA-256 checksum.
3. Create a GitHub Release with a semantic version tag, then upload the archive and checksum file as assets.
4. Update `deploy/artifacthub/headlamp-plugin/headlamp-plugin-dns-api/artifacthub-pkg.yml` so `version`, `createdAt`, archive URL, checksum, and compatibility annotations match the release.
5. Merge `deploy/artifacthub/headlamp-plugin/` metadata to the default branch.
6. In the Artifact Hub control panel, register `deploy/artifacthub/headlamp-plugin/` as a repository with kind `Headlamp plugin`. After the first registration, Artifact Hub scans update the package.

Being indexed by Artifact Hub is not the same as being shown in the default Headlamp Desktop Plugin Catalog. The default catalog may show only plugins marked Official in Artifact Hub or allow-listed by the Headlamp project. If showing the plugin in the default Plugin Catalog is a release requirement, the release checklist includes Artifact Hub Verified publisher setup and the Headlamp project process for allow-listing or Official treatment. Users may disable the `Only official plugins`-style filter to view non-official Artifact Hub plugins, but this is not the standard path for general users.

The Artifact Hub package description and `README.md` state that the plugin calls only the Kubernetes API, does not call AWS APIs, and does not handle provider credentials in Headlamp Desktop. They also state that dns-api CRDs, the controller, and the Route 53 provider must already be installed in the target cluster. Installing the plugin from the Plugin Catalog does not install the dns-api controller or CRDs.

## GitHub Actions

Catalog publishing for the Headlamp plugin is managed by reproducible GitHub Actions workflows. GitHub Actions handles build, validation, release asset creation, and Artifact Hub metadata consistency checks. Artifact Hub repository registration, Verified publisher setup, and Headlamp project allow-list or Official requests are external human actions and are not performed by GitHub Actions.

Pull requests and default-branch pushes run the `headlamp-plugin-ci` workflow. This workflow does not create releases. It only validates that the package and metadata are in a releasable state.

`headlamp-plugin-ci` does the following:

1. Check out the repository.
2. Prepare Bun and Node.js with devbox or the repository standard toolchain.
3. Run `task ui:install`.
4. Run `task headlamp-plugin:check`.
5. Run `task headlamp-plugin:package`.
6. Verify that the archive root is `headlamp-plugin-dns-api/` and that it contains `package.json` and `main.js`.
7. Verify that the archive does not contain `node_modules/`, `src/`, `.map` files, local config, secrets, or test output. If source maps become part of the distribution design, revise this rule in the same design change.
8. Verify that a `.sha256` file exists and matches the archive SHA-256.
9. If `deploy/artifacthub/headlamp-plugin/headlamp-plugin-dns-api/artifacthub-pkg.yml` exists, validate required fields and Headlamp annotations.
10. Verify that `artifacthub-pkg.yml` `headlamp/plugin/archive-url` is a GitHub Releases URL and does not point to a custom CDN, container registry, npm registry, or project website.
11. Verify that `headlamp/plugin/version-compat` matches the Headlamp Desktop target minor version in this design.
12. Verify that `headlamp/plugin/distro-compat` does not include unverified distributions. Initially only `app,mac,linux` is allowed.

`headlamp-plugin-ci` uses minimal GitHub Actions permissions:

```yaml
permissions:
  contents: read
```

`headlamp-plugin-ci` does not write to GitHub Releases, Artifact Hub, npm registries, or container registries. It uses only checks that do not require secrets, including pull requests from forks.

Pushing a semantic version tag `v*` runs the `headlamp-plugin-release` workflow. This workflow publishes the Catalog archive as a GitHub Release asset. Tag names use a `v` prefix, such as `v0.1.0`. The plugin package version and Artifact Hub package version match the tag without the `v` prefix.

`headlamp-plugin-release` does the following:

1. Check out the repository.
2. Prepare Bun and Node.js with devbox or the repository standard toolchain.
3. Run `task ui:install`.
4. Run `task headlamp-plugin:check`.
5. Compute the release version from the tag.
6. Verify that `app/headlamp-plugin/package.json` `version` matches the release version.
7. Run `task headlamp-plugin:package` with the release version and create `headlamp-plugin-dns-api-v<version>.tar.gz` and `.sha256`.
8. Verify archive structure, forbidden files, and checksum using the same rules as `headlamp-plugin-ci`.
9. Verify that `deploy/artifacthub/headlamp-plugin/headlamp-plugin-dns-api/artifacthub-pkg.yml` `version`, archive URL, archive checksum, and compatibility annotations match the release asset.
10. Create the GitHub Release if it does not exist, or reuse the existing release for the same tag.
11. Upload the archive and `.sha256` as GitHub Release assets.

`headlamp-plugin-release` does not automatically commit Artifact Hub metadata. Before creating the release tag, metadata must already match the release version on the default branch. If metadata updates are automated later, that automation is a separate manual workflow that creates a PR updating `artifacthub-pkg.yml`. The PR is merged before the release tag is created.

`headlamp-plugin-release` uses these GitHub Actions permissions:

```yaml
permissions:
  contents: write
```

`headlamp-plugin-release` does not require `id-token: write`. Headlamp plugin release does not use AWS, GCP, npm, or container registry credentials. GitHub Release asset upload uses only the repository `GITHUB_TOKEN`.

The workflow files are `.github/workflows/headlamp-plugin-ci.yml` and `.github/workflows/headlamp-plugin-release.yml`. Even if the repository has a broader CI workflow, Headlamp plugin release asset validation and Artifact Hub metadata validation must remain visible as separate job names.

The release asset created by `headlamp-plugin-release` is the canonical artifact for Catalog publishing. Local archives, temporary CI artifacts, and package registry tarballs are not used as Catalog metadata archive URLs.

## Headlamp Platform Implementation

`app/headlamp-plugin/src/platform.ts` implements the `DnsApiPlatform` interface from the `ui` package.

The Headlamp platform implementation is responsible for:

- Listing, getting, creating, updating, deleting, and watching DNS API resources through Headlamp / Kubernetes APIs.
- Pre-checking operation permissions through `SelfSubjectAccessReview`.
- Navigation through the Headlamp router.
- Clipboard copy through Headlamp or browser APIs.
- Success, warning, and error notifications through the Headlamp notification UI.
- Links to open live object YAML.

The Headlamp platform implementation does not call DNS provider APIs such as Route 53 directly. Headlamp Desktop does not hold AWS credentials, cloud account credentials, or provider SDK configuration. External provider reconciliation is always performed by the Kubernetes controller.

The Headlamp platform acts on the currently selected Headlamp cluster. Namespace visibility and create, update, delete permissions follow Kubernetes RBAC. The plugin does not bypass RBAC. Operations for which the user lacks permissions are hidden or disabled. Main create, update, and delete buttons are pre-checked with `SelfSubjectAccessReview`. Because permissions can change after pre-check, final operation success or failure is still determined by the Kubernetes API, admission webhook, and controller conditions.

## Headlamp Theme Conversion

`app/headlamp-plugin/src/theme.ts` converts Headlamp theme data into the `DnsTheme` type from the `ui` package. Components in the `ui` package must not reference Headlamp theme objects directly.

Headlamp theme conversion maps colors, density, and focus rings into DNS API UI theme tokens so the DNS API UI feels natural inside Headlamp. DNS API UI does not use Headlamp components, but it should match Headlamp appearance when hosted on the Headlamp platform.

## Desktop-Specific Behavior

Public distribution is through the Headlamp Desktop Plugin Catalog. Manual installation archives remain for development, smoke testing, and limited distribution before Catalog listing. The plugin may add Desktop app menu items for opening the current DNS screen and opening the selected resource manifest. It does not provide local command execution, AWS CLI execution, `kubectl apply` delegation, or local kubeconfig modification.

Plugin settings are limited to UI display. Examples include the default display namespace, condition reasons treated as warnings on the Overview page, and manifest preview display format. Plugin settings do not change API behavior, provider credentials, or controller reconciliation behavior.

The Desktop plugin directory can be overridden with `HEADLAMP_PLUGINS_DIR`. If unset, macOS and Linux use `$HOME/.config/Headlamp/plugins`. Windows `%APPDATA%/Headlamp/Config/plugins` is not a standard initial task target.

## Development Commands

Headlamp plugin and UI preview operations are exposed through Taskfile. The root `package.json` is not the developer-facing command entry point.

Taskfile provides:

- `task ui:install`: run `bun install` at the root workspace.
- `task dns-api-ui:mockup`: start the Vite dev server for `app/ui-mockup/` so the `pkg/ts/ui/` package can be previewed visually and interactively.
- `task headlamp-plugin:start`: run `app/headlamp-plugin/` in Headlamp Desktop plugin development mode.
- `task headlamp-plugin:build`: build the Headlamp plugin bundle.
- `task headlamp-plugin:package`: create the Headlamp plugin archive and SHA-256 checksum. The archive is used both as the Catalog release asset and for Desktop manual installation.
- `task headlamp-plugin:check`: run workspace format, lint, type check, and tests.
- `task headlamp-plugin:install-desktop`: extract the packaged plugin into the Headlamp Desktop plugin directory.
- `task headlamp-plugin:clean-desktop`: remove `headlamp-plugin-dns-api` from the Headlamp Desktop plugin directory.
- `task headlamp-plugin:artifacthub-metadata`: update or validate `deploy/artifacthub/headlamp-plugin/headlamp-plugin-dns-api/artifacthub-pkg.yml` from release version, archive URL, and checksum. The implementation may generate metadata or only validate it, but CI must detect mismatches between Artifact Hub metadata and release assets.

The package manager lockfile source of truth is the repository root `bun.lock`. Do not create separate lockfiles under `pkg/ts/ui/`, `app/headlamp-plugin/`, or `app/ui-mockup/`. Do not create `package-lock.json`, `yarn.lock`, or `pnpm-lock.yaml`. If Headlamp plugin tooling generates an npm lockfile as a template artifact, remove it and resolve dependencies through root workspace `bun install`.

The standard Headlamp plugin verification path is: run `task up` to start the kind cluster, CRDs, webhook, and controller; select that kube context in Headlamp Desktop; then run `task headlamp-plugin:start` to load the plugin in development mode. Packaged plugin smoke testing uses `task headlamp-plugin:package` and `task headlamp-plugin:install-desktop`. Before Catalog release, verify the exact archive that will be uploaded to GitHub Releases by installing it with `install-desktop`; this exercises the same runtime bundle as Plugin Catalog installation for Headlamp Desktop `v0.42.x`.
