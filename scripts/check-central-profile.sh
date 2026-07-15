#!/usr/bin/env bash
set -euo pipefail

chart="${1:-deploy/charts/dns-api}"
scratch="$(mktemp -d /tmp/dns-api-central-profile.XXXXXX)"
trap 'rm -rf "$scratch"' EXIT

helm template dns-api "$chart" \
  --namespace dns-api-system \
  --set gatewayEndpoint.enabled=false \
  --set serviceEndpoint.enabled=false \
  --show-only templates/rbac.yaml >"$scratch/rbac.yaml"

helm template dns-api "$chart" \
  --namespace dns-api-system \
  --set gatewayEndpoint.enabled=false \
  --set serviceEndpoint.enabled=false \
  --show-only templates/deployment.yaml >"$scratch/deployment.yaml"

if grep -Eq 'gateway\.networking\.k8s\.io|httproutes|^[[:space:]]+- services$|services/finalizers' "$scratch/rbac.yaml"; then
  echo "central profile grants tenant source-discovery RBAC" >&2
  exit 1
fi

grep -q 'endpointrecordsets' "$scratch/rbac.yaml"
grep -q -- '--gateway-endpoint-controller-enabled=false' "$scratch/deployment.yaml"
grep -q -- '--service-endpoint-controller-enabled=false' "$scratch/deployment.yaml"
