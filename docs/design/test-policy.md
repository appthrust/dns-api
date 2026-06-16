# Test Policy

Acceptance tests use appthrust/kest. kest checks representative success paths only. Exhaustive error cases, detailed validation, and reconciler branches are covered by Go unit tests and controller-runtime envtest.

The core API will eventually provide conformance tests for provider controllers. Conformance tests are black-box tests against real clusters with provider controllers installed. They validate only behavior observable through the Kubernetes API, dns-api resource status, and authoritative DNS queries. Provider cloud API clients or fake clients are not injected into conformance tests. Fake providers are used in envtest and provider implementation internal tests.

Conformance test distribution, execution method, configuration file schema, and support level representation are not yet designed and are not part of the initial implementation. The initial implementation focuses on Route 53 provider envtest and kest success paths.

envtest is used as integration testing with Kubernetes API server, etcd, CRDs, admission webhook, and reconciler. envtest does not operate real Route 53 or other external resources. Provider calls are replaced with fake or mock implementations. It checks Kubernetes resources, conditions, events, and finalizers. For TXT records, envtest or provider unit tests cover `spec.txt.values` validation, `_` in record names, core rejection of newline, tab, control-character, and non-ASCII TXT values through `forbid-txt-non-printable-ascii`, and Route 53 quote / escape / 255-octet chunk comparison as logical TXT values. For CNAME records, envtest or provider unit tests cover zone apex rejection, target syntax validation, standard-body exclusivity, and CNAME same-name conflict with other record types. For MX records, envtest or provider unit tests cover preference range validation, exchange syntax validation, duplicate pair rejection, Null MX validation, standard-body exclusivity, and Route 53 value parsing as unordered preference / exchange pairs. For CAA records, envtest or provider unit tests cover flags range validation, tag grammar validation, value presence, duplicate tuple rejection, standard-body exclusivity, and Route 53 quote / escape parsing as unordered flags / tag / value tuples. For delegated NS records, envtest or provider unit tests cover apex rejection, wildcard rejection, name server syntax validation, duplicate rejection, standard-body exclusivity, and Route 53 name server value comparison as unordered DNS name sets.

Provider validation tests cover the `forbid-txt-non-printable-ascii` disable switch as a core named validation. The built-in Route 53 and Cloudflare Provider manifests do not disable it. A provider test fixture may disable it to prove that the core webhook can be relaxed by a future provider version after that provider defines escape, unescape, display, and comparison behavior. Route 53 provider unit tests keep a defensive controller-side rejection path for non-printable ASCII TXT values even though the built-in webhook path rejects them earlier.

envtest or webhook unit tests cover status subresource validation. For `RecordSet/status`, tests reject `status.observedGeneration` values greater than the `RecordSet.metadata.generation`, reject `status.zone.ref` namespace or name values that differ from `spec.zoneRef` after namespace defaulting, and reject any `status.zone.controllerName` field. Tests also accept a valid `status.zone.ref` as the controller-resolved `Zone` reference. CRD schema or generated-type checks must show that `RecordSet.status.zone.controllerName` is absent from the API surface.

Cloudflare Provider schema validation is covered by admission, webhook unit tests, envtest, and provider controller unit tests. Admission or webhook unit tests reject `Zone.spec.adoption.zoneID`, `RecordSet.spec.adoption.recordIDs[]`, `Zone.status.provider.data.zone.id`, and `RecordSet.status.provider.data.records[].id` when an ID is uppercase, non-hex, shorter than 32 characters, or longer than 32 characters. They also reject duplicate `RecordSet.spec.adoption.recordIDs[]` values and duplicate `RecordSet.status.provider.data.records[]` ID objects. Positive tests accept 32-character lowercase hexadecimal IDs. envtest covers the same Provider schema paths through the Kubernetes API server and status subresources, not only direct schema helpers.

Cloudflare provider controller unit tests cover defensive validation of Cloudflare API responses before status writes. Fake Cloudflare responses that contain a missing ID, uppercase ID, non-hex ID, short or long ID, or duplicate DNS record IDs must not produce invalid `Zone.status.provider.data` or `RecordSet.status.provider.data`. The affected resource reports `Programmed=False` with the provider error or mismatch reason defined by the Cloudflare provider design. These tests cover created resources, adopted resources, and re-observation of already managed resources.

Real-cluster Cloudflare checks verify that IDs returned by the live Cloudflare API and projected to `Zone.status.provider.data.zone.id` and `RecordSet.status.provider.data.records[].id` are 32-character lowercase hexadecimal strings, and that RecordSet status IDs are unique. The live checks do not inject malformed provider responses; malformed response handling remains in unit tests and envtest with fake providers.

`task test:kest` runs the Route 53 kest success paths. It assumes `task up` has started a kind cluster, CRDs, webhook, and controller, and that controller Pod runtime credentials are available through IRSA, `local-irsa`, or an explicit fallback Secret. `task test:kest:cloudflare` runs only Cloudflare kest success paths. The test runner runs appthrust/kest TypeScript tests from the host with Bun. kest TypeScript tests are Bun-only; do not run them with Node.js, ts-node, tsx, or another TypeScript runtime.

## Initial kest Test Cases

Initial Route 53 kest test cases are A record lifecycle, AAAA record lifecycle, TXT record lifecycle, CNAME record lifecycle, MX record lifecycle, CAA record lifecycle, delegated NS record lifecycle, and Route 53 ALIAS record lifecycle. Files are `tests/e2e/recordset-api/route53-a-record-lifecycle.test.ts`, `tests/e2e/recordset-api/route53-aaaa-record-lifecycle.test.ts`, `tests/e2e/recordset-api/route53-txt-record-lifecycle.test.ts`, `tests/e2e/recordset-api/route53-cname-record-lifecycle.test.ts`, `tests/e2e/recordset-api/route53-mx-record-lifecycle.test.ts`, `tests/e2e/recordset-api/route53-caa-record-lifecycle.test.ts`, `tests/e2e/recordset-api/route53-ns-record-lifecycle.test.ts`, and `tests/e2e/recordset-api/route53-alias-record-lifecycle.test.ts`. Each file contains one scenario.

Initial Cloudflare kest test case is Cloudflare Zone lifecycle. The file is `tests/e2e/cloudflare/cloudflare-zone-lifecycle.test.ts`. It creates a Cloudflare-backed `Zone` through Kubernetes resources, verifies the Cloudflare API state, and deletes the `Zone`.

Cloudflare RecordSet kest test cases are added under `tests/e2e/cloudflare` when the Cloudflare ZoneUnit controller is implemented. Files are `tests/e2e/cloudflare/cloudflare-a-record-lifecycle.test.ts`, `tests/e2e/cloudflare/cloudflare-aaaa-record-lifecycle.test.ts`, `tests/e2e/cloudflare/cloudflare-txt-record-lifecycle.test.ts`, `tests/e2e/cloudflare/cloudflare-cname-record-lifecycle.test.ts`, `tests/e2e/cloudflare/cloudflare-mx-record-lifecycle.test.ts`, `tests/e2e/cloudflare/cloudflare-caa-record-lifecycle.test.ts`, `tests/e2e/cloudflare/cloudflare-ns-record-lifecycle.test.ts`, and `tests/e2e/cloudflare/cloudflare-a-proxied-record-lifecycle.test.ts`. Each file contains one scenario.

A record lifecycle represents a typical platform engineer and application engineer user journey. It verifies platform boundary, application boundary, external Route 53 side effects, and cleanup as API contract, not controller internals.

AAAA record lifecycle verifies standard `spec.aaaa.addresses`, Route 53 AAAA representation, IPv6 value comparison, and a representative update path for Route 53 record sets. It creates a Route 53 AAAA record set, updates the address and TTL on the same Kubernetes `RecordSet`, confirms the old Route 53 value is gone and the new value is present, and then deletes the `RecordSet`. The assertion compares parsed IPv6 values, not raw text form.

TXT record lifecycle verifies TXT-specific `spec.txt.values`, record names containing `_`, and Route 53 TXT representation. Hosted zone and record creation are not split into separate tests because `RecordSet` requires `Zone`, and cleanup must delete `RecordSet` then `Zone`. Each is one lifecycle scenario.

CNAME record lifecycle verifies standard `spec.cname.target` and Route 53 CNAME representation. It does not cover zone apex rejection, CNAME same-name conflict, CNAME target syntax failures, or CNAME coexistence with other record types. Those are covered by webhook, envtest, or provider unit tests.

MX record lifecycle verifies standard `spec.mx.records` and Route 53 MX representation. It does not cover invalid preference ranges, exchange syntax failures, or duplicate pair rejection. Those are covered by webhook, envtest, or provider unit tests.

CAA record lifecycle verifies standard `spec.caa.records` and Route 53 CAA representation. It does not cover invalid flag ranges, invalid tag grammar, empty values, duplicate tuple rejection, or CA-specific policy semantics. Those are covered by webhook, envtest, provider unit tests, or manual CA-side checks.

Delegated NS record lifecycle verifies standard `spec.ns.nameServers` and Route 53 delegated NS representation. It does not cover apex rejection, wildcard rejection, name server syntax failures, duplicate rejection, authoritative delegation correctness, glue records, or resolver reachability. Those are covered by webhook, envtest, provider unit tests, or external DNS operations checks.

Route 53 ALIAS record lifecycle verifies that dns-api can create an A alias record set in Route 53 from explicit `spec.options.alias` desired state. It uses another A `RecordSet` in the same hosted zone as the alias target. It does not create or inspect Elastic Load Balancers, CloudFront distributions, S3 buckets, API Gateway domains, Kubernetes `Service`, or Kubernetes `Ingress` resources. Alias target discovery from other AWS or Kubernetes resources is outside the Route 53 controller responsibility and is not part of kest.

Cloudflare Zone lifecycle verifies that dns-api can create and delete a Cloudflare `full` zone through the Cloudflare ZoneUnit controller. It does not create Cloudflare DNS records, does not query public DNS resolvers, and does not require Cloudflare `zone.status=active`. Cloudflare `zone.status=pending` is successful when the observed zone account, name, type, assigned name servers, and `status.provider.data.zone.{id,status,type}` match the design.

Cloudflare RecordSet lifecycles use the same Cloudflare `Zone` setup. They verify that dns-api can create, observe, update, and delete Cloudflare DNS records through Kubernetes `RecordSet` resources. Initial Cloudflare RecordSet kest scenarios cover A, AAAA, TXT, CNAME, MX, CAA, and delegated NS. A separate Cloudflare A proxied lifecycle verifies `spec.options.ttl=Auto`, `spec.options.proxied=true`, `comment`, and `tags` when the selected Cloudflare account supports tags. Provider admission, envtest, or provider unit tests cover Cloudflare fixed TTL validation for the `60..86400` range, rejection of `spec.ttl: 1`, mutual exclusion of `spec.ttl` and `spec.options.ttl`, and acceptance of `spec.options.ttl=Auto`. The test does not query public DNS resolvers and does not require the Cloudflare zone to become publicly delegated.

Cloudflare RecordSet lifecycle assertions use the Cloudflare DNS Records API, not public DNS resolution. Tests read `Zone.status.provider.data.zone.id` and use that Cloudflare zone ID for provider assertions. They list or fetch DNS records by Cloudflare record ID, owner name, and type. Assertions compare unordered record sets because dns-api and Cloudflare do not guarantee value order.

Cloudflare RecordSet lifecycle assertions also confirm that `RecordSet.status.provider.data.records[].id` values are unique 32-character lowercase hexadecimal strings. Cloudflare Zone lifecycle assertions confirm that `Zone.status.provider.data.zone.id` is a 32-character lowercase hexadecimal string. These are success-path checks for the public status contract; invalid ID rejection is covered by admission, webhook unit tests, envtest, and provider controller unit tests.

## kest Scenario Steps

A record lifecycle steps:

1. Create a platform namespace with `newNamespace({ generateName: "dns-api-platform-" })`.
2. Create an application namespace with `newNamespace({ generateName: "dns-api-app-" })`.
3. Create `Route53Identity` and `ZoneClass` in the platform namespace. `Provider` is a cluster-scoped provider capability installed by `task up`.
4. Allow the application namespace through `ZoneClass.spec.allowedZones`.
5. Generate a unique hosted zone value with `s.generateName("hz-")` and set `Zone.spec.domainName` to `<generated>.dns-api.test`. `.test` is used as the test suffix and is not expected to resolve publicly.
6. Create a fixed-name `Zone` in the application namespace referencing the platform `ZoneClass`.
7. Wait for `Zone` `Accepted=True` and `Programmed=True`. Timeout is 5 minutes.
8. Read `Zone.status.provider.data.hostedZoneID` and confirm the Route 53 hosted zone exists with AWS SDK for JavaScript v3 `GetHostedZone` or `ListResourceRecordSets`.
9. Create a fixed-name `RecordSet` in the application namespace with `spec.type=A`, `spec.name=www`, `spec.ttl=300`, and `spec.a.addresses=["192.0.2.10"]`.
10. Wait for `RecordSet` `Accepted=True` and `Programmed=True`. Timeout is 5 minutes.
11. Confirm with AWS SDK for JavaScript v3 `ListResourceRecordSets` that `www.<domainName>` A record set exists.
12. As the application engineer action, delete the `RecordSet` and wait for it to disappear from the Kubernetes API.
13. Confirm with `ListResourceRecordSets` that the A record set was deleted.
14. As the application engineer action, delete the `Zone` and wait for it to disappear from the Kubernetes API.
15. Confirm with `GetHostedZone` that the hosted zone does not exist.

TXT lifecycle uses the same setup and cleanup. The RecordSet creation step creates a fixed-name `RecordSet` in the application namespace with `spec.type=TXT`, `spec.name=_acme-challenge`, `spec.ttl=300`, and `spec.txt.values=["challenge-token", "v=spf1 include:_spf.example.net ~all"]`. After `Accepted=True` and `Programmed=True`, it confirms a TXT record set for `_acme-challenge.<domainName>` with `ListResourceRecordSets`. Returned Route 53 quoted values are parsed to logical TXT values before comparison; raw quote and chunk boundaries are not compared. Deletion confirms the TXT record set disappears.

AAAA lifecycle uses the same setup and cleanup. The RecordSet creation step creates a fixed-name `RecordSet` in the application namespace with `spec.type=AAAA`, `spec.name=ipv6`, `spec.ttl=300`, and `spec.aaaa.addresses=["2001:db8::10"]`. After `Accepted=True` and `Programmed=True`, it confirms a AAAA record set for `ipv6.<domainName>` with one IPv6-equivalent Route 53 value. The update step changes `spec.ttl` to `600` and `spec.aaaa.addresses` to `["2001:db8::20"]`, waits again for `Accepted=True` and `Programmed=True`, and confirms Route 53 contains only the updated IPv6 value and TTL. Deletion confirms the AAAA record set disappears.

CNAME lifecycle uses the same setup and cleanup. The RecordSet creation step creates a fixed-name `RecordSet` in the application namespace with `spec.type=CNAME`, `spec.name=www`, `spec.ttl=300`, and `spec.cname.target=target.example.net`. After `Accepted=True` and `Programmed=True`, it confirms a CNAME record set for `www.<domainName>` with one Route 53 `ResourceRecords[].Value` equal to `target.example.net.`. Deletion confirms the CNAME record set disappears.

MX lifecycle uses the same setup and cleanup. The RecordSet creation step creates a fixed-name `RecordSet` in the application namespace with `spec.type=MX`, `spec.name=@`, `spec.ttl=300`, and `spec.mx.records=[{preference: 10, exchange: "mail1.example.net"}, {preference: 20, exchange: "mail2.example.net"}]`. After `Accepted=True` and `Programmed=True`, it confirms an MX record set for `<domainName>` with Route 53 `ResourceRecords[].Value` containing `10 mail1.example.net.` and `20 mail2.example.net.`. Returned Route 53 MX values are parsed to preference / exchange pairs and compared as an unordered set. Deletion confirms the MX record set disappears.

CAA lifecycle uses the same setup and cleanup. The RecordSet creation step creates a fixed-name `RecordSet` in the application namespace with `spec.type=CAA`, `spec.name=@`, `spec.ttl=300`, and `spec.caa.records=[{flags: 0, tag: "issue", value: "letsencrypt.org"}, {flags: 0, tag: "iodef", value: "mailto:security@example.com"}]`. After `Accepted=True` and `Programmed=True`, it confirms a CAA record set for `<domainName>` with Route 53 `ResourceRecords[].Value` containing `0 issue "letsencrypt.org"` and `0 iodef "mailto:security@example.com"`. Returned Route 53 CAA values are parsed to flags / tag / value tuples and compared as an unordered set. Deletion confirms the CAA record set disappears.

Delegated NS lifecycle uses the same setup and cleanup. The RecordSet creation step creates a fixed-name `RecordSet` in the application namespace with `spec.type=NS`, `spec.name=delegated`, `spec.ttl=300`, and `spec.ns.nameServers=["ns-111.example-dns.net", "ns-222.example-dns.net"]`. After `Accepted=True` and `Programmed=True`, it confirms an NS record set for `delegated.<domainName>` with Route 53 `ResourceRecords[].Value` containing `ns-111.example-dns.net.` and `ns-222.example-dns.net.`. Returned Route 53 NS values are normalized and compared as an unordered set. Deletion confirms the delegated NS record set disappears. The test does not query resolvers or prove the delegated child zone exists.

Route 53 ALIAS lifecycle uses the same platform namespace, application namespace, `Route53Identity`, `ZoneClass`, and `Zone` setup. After the `Zone` is `Accepted=True` and `Programmed=True`, it reads `Zone.status.provider.data.hostedZoneID`. It first creates a fixed-name target A `RecordSet` in the application namespace with `spec.type=A`, `spec.name=target`, `spec.ttl=300`, and `spec.a.addresses=["192.0.2.10"]`, then waits for the target `RecordSet` to become `Accepted=True` and `Programmed=True`. It then creates a fixed-name alias A `RecordSet` with `spec.type=A`, `spec.name=alias`, no `spec.ttl`, no `spec.a`, and `spec.options.alias` set to `dnsName: target.<domainName>.`, `hostedZoneID: <Zone.status.provider.data.hostedZoneID>`, and `evaluateTargetHealth: false`. After the alias `RecordSet` is `Accepted=True` and `Programmed=True`, it confirms with AWS SDK for JavaScript v3 `ListResourceRecordSets` that `alias.<domainName>` exists as an A record set with `AliasTarget.DNSName=target.<domainName>.`, `AliasTarget.HostedZoneId=<Zone.status.provider.data.hostedZoneID>`, and `AliasTarget.EvaluateTargetHealth=false`. The assertion also confirms the alias record set does not have `TTL` or `ResourceRecords`. Cleanup deletes the alias `RecordSet` first, then the target `RecordSet`, then the `Zone`, and confirms both Route 53 record sets and the hosted zone disappear.

Cloudflare Zone lifecycle steps:

1. Create a platform namespace with `newNamespace({ generateName: "dns-api-cloudflare-platform-" })`.
2. Create an application namespace with `newNamespace({ generateName: "dns-api-cloudflare-app-" })`.
3. Create a Secret in the platform namespace with `api-token` set from `CF_API_TOKEN`. The test must not print the token.
4. Create `CloudflareIdentity` in the platform namespace with `spec.accessToken.secretRef.name` pointing to that Secret and `key: api-token`.
5. Wait for `CloudflareIdentity` `Accepted=True` and `Ready=True`. Timeout is 5 minutes.
6. Create `ZoneClass` in the platform namespace with `provider.name=cloudflare.dns.appthrust.io`, `provider.version=v1alpha1`, `identityRef.name` pointing to the `CloudflareIdentity`, `zoneCreationPolicy=Create`, `zoneDeletionPolicy=Delete`, and `allowedZones` matching the application namespace.
7. Generate a unique root-domain-shaped domain name in the form `dns-api-local-<yyyymmdd>-<hhmm>-<short-id>.com` for local runs or `dns-api-ci-<yyyymmdd>-<hhmm>-<short-id>.com` for CI runs. Cloudflare accepts this shape as a zone name. Subdomain-shaped zones under an existing domain such as `dns-api-local-<id>.appthrust.io` are not used because Cloudflare rejects them as non-root domains for this account shape. The test does not require parent-zone delegation.
8. Create a `Zone` in the application namespace referencing the Cloudflare `ZoneClass`.
9. Wait for `Zone` `Accepted=True` and `Programmed=True`. Timeout is 5 minutes.
10. Read `Zone.status.provider.data.zone.id`, `zone.status`, `zone.type`, and `Zone.status.nameServers`.
11. Confirm with the Cloudflare API that the zone ID exists, belongs to `CF_ACCOUNT_ID`, has the generated domain name, has `type=full`, and has status `pending` or `active`.
12. Delete the Kubernetes `Zone` and wait for it to disappear from the Kubernetes API.
13. Confirm with the Cloudflare API that the zone ID no longer exists.

Cloudflare RecordSet lifecycle shared setup steps:

1. Run the same platform namespace, application namespace, Secret, `CloudflareIdentity`, `ZoneClass`, generated domain name, and `Zone` setup as the Cloudflare Zone lifecycle.
2. Wait for `CloudflareIdentity` `Accepted=True` and `Ready=True`. Timeout is 5 minutes.
3. Wait for `Zone` `Accepted=True` and `Programmed=True`. Timeout is 5 minutes.
4. Read `Zone.status.provider.data.zone.id` and use it as the Cloudflare DNS Records API `zone_id`.
5. Create one `RecordSet` in the application namespace for the scenario.
6. Wait for the `RecordSet` `Accepted=True` and `Programmed=True`. Timeout is 5 minutes.
7. Read `RecordSet.status.provider.data.records[]` and require one Cloudflare record ID per desired provider-side DNS record.
8. Confirm with the Cloudflare DNS Records API that each status record ID exists in the Cloudflare zone and that the unordered observed provider record set matches the desired `RecordSet`.
9. Update the same Kubernetes `RecordSet` when the scenario defines an update step.
10. Wait again for `Accepted=True` and `Programmed=True`, then confirm through the Cloudflare DNS Records API that stale Cloudflare records are gone and the observed provider record set matches the updated spec.
11. Delete the Kubernetes `RecordSet` and wait for it to disappear from the Kubernetes API.
12. Confirm through the Cloudflare DNS Records API that all Cloudflare record IDs previously observed for the `RecordSet` no longer exist.
13. Delete the Kubernetes `Zone` and wait for it to disappear from the Kubernetes API.
14. Confirm with the Cloudflare API that the zone ID no longer exists.

Cloudflare A lifecycle creates a fixed-name `RecordSet` with `spec.type=A`, `spec.name=www`, `spec.ttl=300`, and `spec.a.addresses=["192.0.2.10", "192.0.2.11"]`. The Cloudflare assertion expects two A records for `www.<domainName>` with `content` values `192.0.2.10` and `192.0.2.11`, `ttl=300`, and `proxied=false`. The update step changes the spec to `spec.ttl=600` and `spec.a.addresses=["192.0.2.12"]`. The updated Cloudflare assertion expects exactly one A record with `content=192.0.2.12`, `ttl=600`, and `proxied=false`.

Cloudflare AAAA lifecycle creates a fixed-name `RecordSet` with `spec.type=AAAA`, `spec.name=ipv6`, `spec.ttl=300`, and `spec.aaaa.addresses=["2001:db8::10"]`. The Cloudflare assertion expects one AAAA record for `ipv6.<domainName>` with an IPv6-equivalent `content` value and `ttl=300`. The update step changes the address to `2001:db8::20` and `ttl=600`; the assertion compares parsed IPv6 values, not raw text form.

Cloudflare TXT lifecycle creates a fixed-name `RecordSet` with `spec.type=TXT`, `spec.name=_acme-challenge`, `spec.ttl=300`, and `spec.txt.values=["challenge-token", "v=spf1 include:_spf.example.net ~all"]`. The Cloudflare assertion expects two TXT records for `_acme-challenge.<domainName>` with the same logical TXT values and `ttl=300`; raw provider quoting or chunk representation is not compared. The update step replaces the TXT values with `["challenge-token-2"]` and verifies that the old TXT records are gone.

Cloudflare CNAME lifecycle creates a fixed-name `RecordSet` with `spec.type=CNAME`, `spec.name=www`, `spec.ttl=300`, and `spec.cname.target=target.example.net`. The Cloudflare assertion expects one CNAME record for `www.<domainName>` with `content=target.example.net` after normalizing trailing root dot differences and `ttl=300`. The update step changes the target to `target2.example.net` and verifies that the same dns-api `RecordSet` now represents the new target.

Cloudflare MX lifecycle creates a fixed-name `RecordSet` with `spec.type=MX`, `spec.name=@`, `spec.ttl=300`, and `spec.mx.records=[{preference: 10, exchange: "mail1.example.net"}, {preference: 20, exchange: "mail2.example.net"}]`. The Cloudflare assertion expects two MX records for `<domainName>` with `content` values `mail1.example.net` and `mail2.example.net`, `priority` values `10` and `20`, and `ttl=300`, compared as an unordered set. The update step changes the records to `[{preference: 5, exchange: "mail.example.net"}]` and verifies that only that MX record remains. Null MX is not part of the Cloudflare kest lifecycle because Cloudflare `v1alpha1` rejects Null MX until Cloudflare API support is verified.

Cloudflare CAA lifecycle creates a fixed-name `RecordSet` with `spec.type=CAA`, `spec.name=@`, `spec.ttl=300`, and `spec.caa.records=[{flags: 0, tag: "issue", value: "letsencrypt.org"}, {flags: 0, tag: "iodef", value: "mailto:security@example.com"}]`. The Cloudflare assertion expects two CAA records for `<domainName>` with matching `data.flags`, `data.tag`, and `data.value` tuples and `ttl=300`, compared as an unordered set. The update step changes the CAA records to `[{flags: 0, tag: "issue", value: "pki.goog"}]`.

Cloudflare delegated NS lifecycle creates a fixed-name `RecordSet` with `spec.type=NS`, `spec.name=delegated`, `spec.ttl=300`, and `spec.ns.nameServers=["ns-111.example-dns.net", "ns-222.example-dns.net"]`. The Cloudflare assertion expects two NS records for `delegated.<domainName>` with matching delegated name server values and `ttl=300`, compared as an unordered set after normalizing trailing root dot differences. The update step changes the delegated name servers to `["ns-333.example-dns.net"]`. The test does not query resolvers, does not prove the delegated child zone exists, and does not inspect Cloudflare zone apex name servers.

Cloudflare A proxied lifecycle creates a fixed-name `RecordSet` with `spec.type=A`, `spec.name=proxy`, no core `spec.ttl`, `spec.options.ttl=Auto`, `spec.options.proxied=true`, `spec.options.comment="proxied app endpoint"`, `spec.options.tags=["app:frontend", "owner:platform"]`, and `spec.a.addresses=["192.0.2.30"]`. The Cloudflare assertion expects one A record for `proxy.<domainName>` with `content=192.0.2.30`, `proxied=true`, and `ttl=1`. If Cloudflare returns comments and tags for the selected account, the test asserts that `comment` and `tags` match the manifest. If Cloudflare rejects comments or tags because the account plan does not support them, the test expects the Kubernetes `RecordSet` to report `Programmed=False`, reason `ProviderInvalidRequest`, and skips the success-path proxied assertion for that account configuration. The update step, when the initial create succeeds, changes `spec.options.comment` and `spec.options.tags` while keeping `proxied=true` and confirms the provider-side metadata changes.

## kest Assertion Scope

kest assertions are limited to API-contract behavior observable by users. Primary Route 53 assertions are `Zone` and `RecordSet` conditions, `status.provider.data.hostedZoneID`, Route 53 hosted zone existence, A record set existence, TXT record set existence, CNAME record set existence, MX record set existence, CAA record set existence, delegated NS record set existence, and Route 53 ALIAS record set existence. Primary Cloudflare Zone assertions are `CloudflareIdentity` conditions, `Zone` conditions, `Zone.status.nameServers`, `status.provider.data.zone.{id,status,type}`, Cloudflare zone existence, and Cloudflare zone deletion. Primary Cloudflare RecordSet assertions are `RecordSet` conditions, `status.provider.data.records[].id`, Cloudflare DNS record existence, content / priority / CAA data matching, TTL matching including `ttl=1` for automatic TTL, proxied state, comment, tags when supported by the account, and Cloudflare DNS record deletion.

`ZoneUnit` ownership keys, stable record-set map behavior, finalizers, and controller internal intermediate states are tested in conformance tests or envtest. Initial kest success paths do not directly inspect internal `ZoneUnit` ownership details.

Failure paths, DNS propagation checks, adoption, conflict, drift, external AWS resource alias targets, alias target discovery, and long TXT value chunking are not part of initial kest scenarios. TXT quote, escape, and 255-octet chunking details are covered by envtest and provider implementation unit tests.

## kest Implementation Rules

Before implementing kest tests, implementers read the appthrust/kest README, especially the Best Practices section. This design explains dns-api application policy but does not replace the kest README. Do not implement kest tests only from a summary of this design without reading the kest README.

kest test names, file names, `given`, `when`, and `then` use API resources and user actions, not controller names. Avoid implementation-oriented wording such as `route53-zone-controller reconciles records`. Prefer `a RecordSet is created`, `the RecordSet reports Programmed=True`, and `the Route 53 record set exists`.

kest manifests are inline TypeScript objects in the test body by default, so the input is visible when reading the test. Do not hide entire manifests behind helper functions. Use static fixture files only when manifests become too large.

Namespaced Kubernetes resources use readable fixed names because namespaces isolate them. `s.generateName` is used only for external names not isolated by namespace, such as hosted zone domain names, or when a temporary cluster-scoped resource is needed.

When adding update scenarios, place the base manifest where it is visible, use `structuredClone`, and explicitly mutate only changed fields. For assertions with incomplete type definitions, avoid unnecessary casts and use `toMatchObject` for the observed subset.

## kest Credentials and Cleanup

`task test:kest` calls Bun's test runner for Route 53 tests. Taskfile runs `bun test tests/e2e/recordset-api` so the default Route 53 path does not require Cloudflare credentials. `task test:kest:cloudflare` runs `bun test tests/e2e/cloudflare` so Cloudflare checks can be run locally and in CI without AWS credentials. CI also gets Bun from devbox.

Local Route 53 kest reads the AWS shared config profile specified by Taskfile variable `AWS_PROFILE` through AWS SDK for JavaScript v3. The standard profile is `appthrust-dns-api-dev`. Taskfile may pass `AWS_PROFILE={{.AWS_PROFILE}}` to the Bun test process when needed, and the user-facing argument is also `AWS_PROFILE`. GitHub Actions Route 53 kest does not use SSO; it reads environment variables set by `aws-actions/configure-aws-credentials` through the AWS SDK default credential chain.

Local Cloudflare kest reads `CF_ACCOUNT_ID` and `CF_API_TOKEN` from the environment. Local users normally run it through `opx devbox run -- task test:kest:cloudflare` so `.env` 1Password references are expanded. CI Cloudflare kest reads `CF_ACCOUNT_ID` and `CF_API_TOKEN` directly from GitHub Actions secrets or environment variables and must not require AWS credentials.

Route 53 `INSYNC` checks wait up to 5 minutes. Short retry intervals are handled by kest action options; fixed sleeps are not used.

kest uses `ZoneClass.spec.parameters.zoneDeletionPolicy=Delete`. Hosted zones created by kest receive standard ownership tags plus `appthrust.io/test-scope=kest` and `appthrust.io/test-id=<generated>` from `ZoneClass.spec.parameters.tags`. The controller has no kest-specific behavior; it only uses normal tag support.

Leftover external resources from failures are not covered by the kest scenario itself. If `KEST_PRESERVE_ON_FAILURE=1` stops cleanup or a failure leaves a hosted zone, manual cleanup uses `appthrust.io/test-scope` and `appthrust.io/test-id`.

Taskfile provides `route53:cleanup-kest-zones`. It uses the AWS shared config profile from `AWS_PROFILE` and targets hosted zones tagged `appthrust.io/test-scope=kest`. By default it only lists deletion targets. Deletion runs only with an explicit flag.

Taskfile provides `cloudflare:cleanup-kest-zones`. It uses `CF_ACCOUNT_ID` and `CF_API_TOKEN`, lists zones in the selected Cloudflare account, and targets only zone names with the effective dns-api test prefix. The default prefix is `dns-api-local-` for the local development Cloudflare account and `dns-api-ci-` for the CI Cloudflare account. A provided `PREFIX` must start with the default prefix and can only narrow the target set. By default the task only lists deletion targets. Deletion runs only when `DELETE=true` or `DELETE=1` and `CONFIRM_PREFIX` exactly equals the effective prefix. It deletes whole matching test zones, not individual DNS records in arbitrary zones. It must not print token values.

Cloudflare cleanup does not use Cloudflare zone tags or DNS record tags as ownership metadata in the initial design. Cloudflare DNS record tags are desired state on managed `RecordSet` resources and may be plan-dependent. The cleanup task must not delete DNS records from existing non-test zones based on tags, comments, names, or record values. If a failed kest run leaves records, the allowed cleanup target is the root-domain-shaped test zone whose name matches the safe prefix.
