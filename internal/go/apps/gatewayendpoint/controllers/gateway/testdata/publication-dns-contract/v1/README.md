# Gateway Fleet to dns-api publication contract (v1)

APT-822 / ADR-0083: these fixtures cross the repository boundary without importing
another repository's `internal` package. They are real source Gateway and
HTTPRoute objects captured after the platform Gateway Fleet `Reconciler` runs,
not hand-authored source status.

## Ownership and regeneration

The producer is platform's
`internal/controllers/gatewayfleet/publication_dns_contract_test.go`,
`TestPublicationDNSContract`. From the platform repository root, regenerate with:

```sh
SERIALIZE_VM_TESTS_TARGET=local ~/.codex/skills/serialize-vm-tests/scripts/serialize-vm-tests -- env UPDATE_PUBLICATION_DNS_CONTRACT=1 go test ./internal/controllers/gatewayfleet -run '^TestPublicationDNSContract$' -count=1
```

Without `UPDATE_PUBLICATION_DNS_CONTRACT=1`, the producer compares its complete
normalized output with the golden JSON and verifies `SHA256SUMS`. The generator
writes nothing until every scenario has been produced successfully.

Copy this **entire directory byte-for-byte**, including this README and
`SHA256SUMS`, to dns-api's
`internal/go/apps/gatewayendpoint/controllers/gateway/testdata/publication-dns-contract/v1/`.
The consumer there is also named `TestPublicationDNSContract`; it verifies the
checksums and the complete 12-scenario inventory before reconciling anything.
A fixture change must land in **both repositories**. Platform's dns-api import
approval PRs under `hack/upstream-artifacts/...` must check that the imported
revision carries the current, byte-identical copy. A green test in only one
repository is not cross-repository approval.

## Wire format

Each JSON document contains `scenario`, `description`, `gateway`, `routes`, and
`expect`. Metadata retains only name, namespace, UID and generation. Gateway spec
and status are captured in full. Routes retain spec hostnames and parentRefs and
status parents. API version and kind are retained. Resource versions, managed
fields and creation timestamps are excluded; condition transition timestamps are
fixed to `2026-01-01T00:00:00Z`. Fixture identities, route order and JSON key order
are deterministic. `SHA256SUMS` covers every JSON fixture (not itself or this
README).

`expect.hostnames` means:

- `retain`: all established names must remain.
- `withdraw-hostnames`: exactly these names must disappear; other names remain.
- `withdraw-all`: all listed established names and the shared set must disappear.

The baseline has 12 source routes and 13 names: `app-00` also publishes
`alias.project.example`. Thus hostname removal tests partial withdrawal within
one route, and route removal/replacement tests unaffected siblings too.

## Current contract, not future wave-3 behavior

The producer captures actual status rather than rewriting it to a desired tuple.
In particular, absent leaf SecurityPolicy status currently closes the edge with
`Accepted=False/RouteClosed` and `PoliciesReady=False/PoliciesNotReady`. This is
the deliberate fence until ADR-0083 wave 3 Q2, not the future `Unknown` behavior.
Under D4, dns-api retains the already receipted hostname for either False or
Unknown route acceptance. Explicit policy rejection has the same DNS outcome.
An explicit observed Gateway rejection instead withdraws the shared set.

The incomplete-pass cases preserve source route status, while Gateway acceptance
can become Unknown. ParentRef removal may leave an old parent status whose
observed generation predates the new route generation: the intent removal, not
that stale status, authorizes DNS withdrawal. APT-1087 backend projection lag is
intentionally not included.

## Consumer restart and provider boundary

The consumer establishes healthy publication using the real gateway-endpoint,
endpointrecordset, ZoneUnit composition and Route 53 reconcilers. It uses the
retention-test harness's HTTP conversion handler and in-memory Route 53 provider.
It requires persisted admission receipts, EndpointRecordSet/child UIDs, and
provider records before taking a snapshot.

Each scenario reconstructs a fresh fake API client and every reconciler from
that snapshot, substitutes only the captured source Gateway/routes, and starts
with a fresh provider adapter holding the existing records. No previous
reconciler or admission cache is reused. Existing source-route incarnations keep
their DNS-owned finalizers. All baseline route keys, including a deleted route's
key, are reconciled. Convergence is bounded to 16 passes and compares all stored
objects, provider records, tags and the provider action log. It ignores only
metadata resourceVersion/managedFields and EndpointRecordSet hostname-condition
lastTransitionTime (the independently tracked APT-1185 status-restamping defect).
Ignored status-only writes are counted and logged for each scenario. Specs,
receipts, annotations, finalizers and every other status field remain compared.

Retain cases require the EndpointRecordSet and every child RecordSet UID/spec to
remain unchanged. Every Route 53 DELETE action is counted, so DELETE followed by
UPSERT cannot hide a transient withdrawal. Withdrawal cases require exactly the
specified provider names and children to disappear, with sibling identities and
records unchanged. Gateway rejection additionally requires the shared
EndpointRecordSet to be gone. No live infrastructure or AWS credentials are used.
