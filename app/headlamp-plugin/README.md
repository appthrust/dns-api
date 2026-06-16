# headlamp-plugin-dns-api

This package contains the dns-api plugin for Headlamp Desktop. The main UI package lives in `pkg/ts/ui/`. This package owns Headlamp registration, routes, sidebar entries, theme conversion, and the platform adapter. Its license is Apache-2.0, the same as the repository root `LICENSE`.

## Commands

Run Taskfile commands from the repository root.

```sh
task ui:install
task headlamp-plugin:start
task headlamp-plugin:check
task headlamp-plugin:build
task headlamp-plugin:package
```

`task headlamp-plugin:package` creates the archive and SHA-256 checksum used both for Catalog release assets and manual Desktop installation. To set the release version:

```sh
task headlamp-plugin:package VERSION=0.1.0
```

To install a packaged plugin into the local Headlamp Desktop plugin directory:

```sh
task headlamp-plugin:install-desktop
```

To use another directory, set `HEADLAMP_PLUGINS_DIR`.

```sh
HEADLAMP_PLUGINS_DIR=/path/to/plugins task headlamp-plugin:install-desktop
```

Catalog publication steps are in `docs/development/headlamp-plugin-release.md`. User installation and troubleshooting are in `docs/manual/headlamp-plugin.md`.
