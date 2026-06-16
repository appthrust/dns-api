# DNS API Headlamp Plugin

DNS API is a Headlamp Desktop plugin for managing dns-api resources from the selected Kubernetes cluster.

The plugin uses only the Kubernetes API exposed by Headlamp. It does not call AWS, Route 53, or any other DNS provider API directly, and it does not store provider credentials in Headlamp Desktop.

Before using the plugin, install the dns-api CRDs, controller, webhook, and Route 53 provider resources in the target cluster. Installing this plugin from the Headlamp Plugin Catalog does not install the dns-api controller or CRDs.

The initial supported runtime is Headlamp Desktop `v0.42.x` on app, macOS, and Linux distributions.

For install notes, supported workflows, and troubleshooting, see `docs/manual/headlamp-plugin.md` in the dns-api repository.
