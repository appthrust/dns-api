# Development Environment Design

This document defines the dns-api repository development environment, development commands, the local kind cluster, Tilt, AWS development credentials, Cloudflare development credentials, `local-irsa`, and GitHub Actions credential setup.

The core DNS API contract, Route 53 provider resource contract, and kest scenarios are defined in [docs/design/README.md](./README.md). The UI package, mockup package, and Headlamp plugin package designs are defined in [docs/design/ui.md](./ui.md) and [docs/design/headlamp-plugin.md](./headlamp-plugin.md).

## Toolchain

The development environment is provided by devbox. Go, kind, kubectl, kustomize, AWS CLI v2, Task, Helm, Tilt, Bun, Node.js, and `flarectl` are provided by devbox. Developers are not expected to install these tools directly on their local machines.

`flarectl` is a Cloudflare development smoke-check tool. It is used to confirm Cloudflare account IDs and API tokens before controller implementation or kest tests use them. The dns-api controller implementation must not shell out to `flarectl`; it uses the Cloudflare API or SDK directly.

The Go version must match across `go.mod`, devbox, and the controller image build. The initial implementation uses Go `1.26.2`. The `go` directive in `go.mod` is `1.26.2`, the Go package in `devbox.json` is `go@1.26.2`, and the Dockerfile builder image is `golang:1.26.2`. The Go version in the Dockerfile must not be older than the version required by `go.mod`. If adding or updating a Go tool dependency such as `local-irsa` raises the required Go version, update `go.mod`, `devbox.json`, and `Dockerfile` in the same change.

Repository development commands are centralized in Taskfile. Do not use a Makefile. The development environment entry point is `task up`. The repository does not provide a development mode such as `task run` that runs the controller as a host-side process.

Kubernetes controller implementation uses controller-runtime. API types, webhooks, reconcilers, and manager setup follow controller-runtime conventions.

## kind / Tilt

Development dependencies installed into Kubernetes are managed by Tilt. Do not use helmfile. Dependency charts such as cert-manager are installed with Tilt's Helm support. Developers must not install dependency charts manually with `helm install`. Go library dependencies are managed by Go modules and are separate from Kubernetes cluster dependencies.

`task up` creates the kind cluster if it does not exist, points kubectl context at the development kind cluster, and runs `tilt up`. Tilt applies CRDs, dependency charts, the controller Deployment, the webhook Service, certificates, and `ValidatingWebhookConfiguration`. The controller and webhook always run inside the cluster. If the Tiltfile composes manifests, it must use the standalone `kustomize` command provided by devbox. Do not use `kubectl kustomize` as a standard fallback. Tilt does not create or update the AWS runtime credentials Secret.

The Docker build context and Tilt watch scope for the `dns-api-controller` image are limited to inputs required to build the controller manager binary. The standard `.dockerignore` has this shape:

```text
*
!Dockerfile
!go.mod
!go.sum
!app/operator/cmd/**
!pkg/go/api/**
!internal/**
```

`docs/design/`, `docs/manual/`, `docs/design-feedback/`, UI packages, the Headlamp plugin package, package-manager artifacts, local state, test output, `.git/`, `.devbox/`, `.task/`, and `tmp/` are not inputs to the controller image. Changes only in these paths must not rebuild the `dns-api-controller` image or redeploy the `dns-api-controller-manager` Deployment.

Tiltfile `docker_build` watches files with the same intent as `.dockerignore`. If it uses additional `ignore` rules, it must not add paths excluded by the `.dockerignore` back into the watch set.

`task up` workflow:

```text
task up
  ensure kind cluster
  set kubectl context
  tilt up
    install cert-manager dependency
    apply CRDs
    apply controller Deployment
    apply webhook Service and certificate
    apply ValidatingWebhookConfiguration
```

Tiltfile workflow:

```text
load kustomize manifests with standalone kustomize
helm_resource cert-manager
k8s_yaml dns-api manifests
docker_build dns-api-controller with narrow context
k8s_resource dns-api-controller-manager
```

## AWS Development Credentials

Route 53 development uses a dedicated AWS development account or sandbox account. Authentication assumes AWS IAM Identity Center SSO or short-lived credentials. Long-lived access keys are not used. AWS provisioning tasks and `local-irsa` tasks receive the AWS shared config profile through the standard `AWS_PROFILE` variable. The standard Route 53 development profile is `AWS_PROFILE=appthrust-dns-api-dev`.

Taskfile runs `local-irsa` as `go tool local-irsa`. `local-irsa` is pinned as a Go tool dependency in `go.mod` so developers can use it inside the devbox shell without installing a separate binary.

Production default manifests assume IRSA or Pod Identity. The default controller Deployment does not read an `aws-runtime-credentials` Secret through `envFrom`. The controller ServiceAccount receives an IRSA role annotation or Pod Identity binding depending on the deployment method. `Route53Identity.spec.credentials.runtime` reads the runtime credential bound to that ServiceAccount through the AWS SDK default credential chain. Secret-based `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, and `AWS_SESSION_TOKEN` are not the production default.

The recommended credential path for local kind development is WebIdentity through `local-irsa`. `task up` does not create AWS IAM resources, S3 buckets, OIDC Providers, IAM Roles, or ServiceAccount annotations. Developers explicitly run the AWS-side and `local-irsa` install / bind tasks when they need Route 53 access.

## local-irsa

`local-irsa:init` has this shape:

```sh
LOCAL_IRSA_NAME=dns-api AWS_REGION=ap-northeast-1 AWS_PROFILE=appthrust-dns-api-dev task local-irsa:init
```

`local-irsa:init` only creates local state and a kind OIDC snippet. It does not create the kind cluster. `kind:create` uses `kind.yaml` when present. Developers using `local-irsa` copy the snippet produced by `local-irsa:init` into `kind.yaml` before creating the cluster. `kind.yaml` is a local file. It must not be committed because it contains AWS account ID, bucket, and issuer URL values.

`local-irsa:install` has this shape:

```sh
LOCAL_IRSA_NAME=dns-api AWS_PROFILE=appthrust-dns-api-dev task local-irsa:install
```

`local-irsa:install` prepares the S3 issuer, IAM OIDC Provider, and Pod Identity Webhook. cert-manager is managed by `task up` / Tilt, so `local-irsa:install` assumes cert-manager already exists.

## Route 53 Controller Policy

The IAM policy document used by the Route 53 controller is stored at `app/operator/config/aws/route53-controller-policy.json`. This JSON is not Taskfile-specific; it is also a neutral policy document that users can attach to production IRSA roles. The policy document must not include account IDs, role names, profiles, local-irsa cluster names, trust policies, or ServiceAccount subjects. Trust policies are managed separately for each local-irsa or production IRSA setup.

`app/operator/config/aws/route53-controller-policy.json` expresses only the Route 53 actions required by the initial Route 53 controller scope: create, read, list, update, and delete public hosted zones and record sets, read and update tags, and check Route 53 changes. AWS IAM actions that do not support resource scoping may use `Resource: "*"`. The policy document must not include permissions to create the local-irsa S3 issuer, create IAM OIDC Providers, edit IAM Role trust policies, or assume cross-account roles. If `Route53Identity.spec.assumeRoleChain` is used, users grant `sts:AssumeRole` permissions to the base role separately for their environment.

Taskfile provides a task that creates or updates this policy in an AWS account. The task name is `aws:ensure-route53-controller-policy` and it accepts at least these variables:

- `AWS_PROFILE`: AWS shared config profile. It is optional and has the explicit default `appthrust-dns-api-dev` for local development tasks.
- `POLICY_NAME`: customer managed policy name. Default: `dns-api-route53-controller`.
- `POLICY_FILE`: IAM policy document path. Default: `app/operator/config/aws/route53-controller-policy.json`.

`aws:ensure-route53-controller-policy` creates the customer managed policy in the AWS account for `AWS_PROFILE` when it is set, or for the ambient AWS credential chain when it is not set. If the policy already exists, it updates the policy document. Updates create a new default policy version and delete only non-default old versions if the AWS version limit is reached. The task prints the final policy ARN to stdout. The task does not edit IAM Role trust policies or ServiceAccount annotations.

`local-irsa:bind-controller` binds the `dns-api-system/dns-api-controller-manager` ServiceAccount to the local-irsa IAM Role. The task uses `AWS_PROFILE` when it is set for both local-irsa and AWS policy ARN resolution. If `CONTROLLER_POLICY_ARN` is set, the task attaches it. If not set, the task resolves the policy ARN from `POLICY_NAME` / `POLICY_FILE` using the same defaults as `aws:ensure-route53-controller-policy`. The role name can be overridden with `CONTROLLER_ROLE_NAME`; otherwise it is a stable name that includes the cluster name. After binding, the task restarts the controller Deployment so the Pod Identity Webhook can inject the WebIdentity token volume and AWS environment into the new Pod.

`local-irsa:doctor` runs `go tool local-irsa doctor --name <LOCAL_IRSA_NAME> --namespace dns-api-system --service-account dns-api-controller-manager --context kind-<KIND_CLUSTER> --profile <AWS_PROFILE>` when `AWS_PROFILE` is set and checks the ServiceAccount binding, issuer, OIDC Provider, and webhook mutation.

`local-irsa:unbind-controller` removes the controller ServiceAccount binding and the IAM Role managed by local-irsa. `local-irsa:down` removes cluster-level AWS resources managed by local-irsa. S3 bucket deletion is destructive, so it only runs when an explicit variable such as `DELETE_BUCKET=true` or `YES=true` is set.

## Fallback Credential

`aws:sync-runtime-credentials` remains as a fallback task for development environments that cannot use `local-irsa`. This task also accepts `AWS_PROFILE` and does not pass an SSO profile itself into a Pod. The `aws-runtime-credentials` Secret created by this fallback task is not referenced from production default manifests. When the fallback is used, a development-only patch or explicit task adds `envFrom` to the controller Deployment.

For local development across multiple AWS accounts, do not pass different profiles into Pods for each account. Use one controller runtime credential as the base credential, and use `Route53Identity.spec.assumeRoleChain` to assume roles in target accounts. The base credential needs `sts:AssumeRole` permission, and target role trust policies must trust the IAM role of the base credential.

## Cloudflare Development Credentials

Cloudflare development uses accounts that are separate from the Cloudflare production account. The production Cloudflare account must not be used by local development, CI, sample `.env` values, Taskfile defaults, or GitHub Actions secrets.

The local development Cloudflare account is `Appthrust dns-api local development account`. Its account ID is `023e105f4ecef8ad9ca31a8372d0c353`. It is used only for developer-local checks, local kest runs, and manual Cloudflare provider experiments.

The CI Cloudflare account is `Appthrust-dns-api-ci@craftsman-software.com's Account`. Its account ID is `023e105f4ecef8ad9ca31a8372d0c354`. It is used only by GitHub Actions and CI-reproducible Cloudflare kest checks.

Cloudflare local credentials are supplied through a local `.env` file read by `opx`. The `.env` file is ignored by git and is normally copied from the committed `.env.dev` template. `.env.dev` must contain only 1Password references, not raw token values. The standard local development values are:

```dotenv
CF_ACCOUNT_ID="op://Private/appthrust-dns-api-dev/cloudflare/account-id"
CF_API_TOKEN="op://Private/appthrust-dns-api-dev/cloudflare/api-token"
```

The standard GitHub Actions 1Password item is `appthrust-dns-api-gha`. It uses a `cloudflare` section with the same field names:

```dotenv
CF_ACCOUNT_ID="op://Private/appthrust-dns-api-gha/cloudflare/account-id"
CF_API_TOKEN="op://Private/appthrust-dns-api-gha/cloudflare/api-token"
```

CI values that are set from a local machine live in `.env.gha`. The `.env.gha` file is committed and may contain only `op://` references and non-secret defaults. It is used only as input when setting GitHub Actions Secrets with `op run --env-file .env.gha -- task github:secrets:set`. GitHub Actions does not run `op run` and does not read `.env.gha` at runtime. Workflows read `CF_ACCOUNT_ID`, `CF_API_TOKEN`, Route 53 account values, and local-irsa values from GitHub Secrets.

Cloudflare API tokens for local and CI use the same minimum initial permissions:

- account permission on the matching local or CI account: `Account Settings:Read`.
- zone permissions on all zones from the same account: `Zone:Edit` and `DNS:Edit`.

The token account resource scope includes exactly the matching local or CI account. It must not include all accounts. The zone resource scope is `All zones from an account`, limited to the same account. Tokens must not include Global API Key access.

The account read permission is required because `CloudflareIdentity` calls `GET /accounts` and requires exactly one visible account before setting `Ready=True`. A token with only zone and DNS edit permission may be valid for mutation but is not sufficient for dns-api identity readiness.

The Cloudflare credential smoke check is:

```sh
opx devbox run -- flarectl --json zone list
```

For a newly created development or CI account, the expected smoke-check output is an empty JSON array:

```json
[]
```

`flarectl` reads `CF_ACCOUNT_ID` and `CF_API_TOKEN`. A successful `zone list` confirms that `opx` can expand 1Password references, the token authenticates, and the token can list zones in the selected Cloudflare account. It does not prove that the dns-api Cloudflare controller works.

Taskfile commands that use Cloudflare credentials must make the target account explicit. Local tasks read `CF_ACCOUNT_ID` and `CF_API_TOKEN` from `.env` through `opx` or from the process environment. CI tasks read `CF_ACCOUNT_ID` and `CF_API_TOKEN` from GitHub Actions secrets or environment variables. No task may infer a Cloudflare account by listing all accessible accounts.

## Cloudflare Local Integration Tasks

Cloudflare local integration checks use the local Cloudflare account and a local kind cluster. They verify the dns-api controller against the real Cloudflare API. They are separate from the `cloudflare:smoke-check` task, which verifies only `flarectl` credentials.

The Taskfile must provide these Cloudflare local tasks:

- `cloudflare:smoke-check`: verify `CF_ACCOUNT_ID` and `CF_API_TOKEN` can list zones with `flarectl --json zone list`.
- `cloudflare:sync-token-secret`: create or update a Kubernetes Secret containing the Cloudflare API token for the controller to read through `CloudflareIdentity.spec.accessToken.secretRef`.
- `cloudflare:dev:apply-platform`: apply a local platform namespace, Cloudflare token Secret, `CloudflareIdentity`, and Cloudflare `ZoneClass`.
- `cloudflare:dev:zone-lifecycle`: run a manual local zone lifecycle check through Kubernetes resources and Cloudflare API assertions.
- `test:kest:cloudflare`: run only Cloudflare kest tests.
- `ci:cloudflare:kest`: set up CI prerequisites and run only Cloudflare kest tests.
- `cloudflare:cleanup-kest-zones`: list Cloudflare test zones and delete them only when an explicit delete flag is set.

`cloudflare:sync-token-secret` behavior:

- reads `CF_API_TOKEN` from the process environment. Local users normally run it as `opx devbox run -- task cloudflare:sync-token-secret`.
- does not print `CF_API_TOKEN` or 1Password-resolved token values.
- creates the target namespace if needed.
- creates or updates a Secret with default name `cloudflare-api-token`.
- stores the token under default key `api-token`.
- allows overriding namespace, Secret name, and key through Task variables.
- must not write the token to repository files, generated manifests, shell history helper files, or Taskfile defaults.

`cloudflare:dev:apply-platform` behavior:

- depends on `cloudflare:sync-token-secret`.
- creates a platform namespace, default `dns-api-cloudflare-platform`.
- labels the application namespace selector used by the local `ZoneClass` explicitly instead of opening the class to all namespaces.
- creates `CloudflareIdentity` with default name `cloudflare-local`.
- creates `ZoneClass` with default name `cloudflare-public`.
- sets `ZoneClass.spec.provider.name=cloudflare.dns.appthrust.io`.
- sets `ZoneClass.spec.provider.version=v1alpha1`.
- sets `ZoneClass.spec.controllerName=cloudflare.dns.appthrust.io/controller`.
- sets `ZoneClass.spec.identityRef.name=cloudflare-local`.
- sets `ZoneClass.spec.parameters.zoneCreationPolicy=Create`.
- sets `ZoneClass.spec.parameters.zoneDeletionPolicy=Delete` for integration checks so external zones are cleaned when the Kubernetes `Zone` is deleted.

The local Cloudflare kind setup must not require AWS, `local-irsa`, AWS SSO, or Route 53 credentials. The Taskfile must provide a Cloudflare-specific kind path for local or CI use that creates/selects kind, installs cert-manager, builds and loads the controller image, applies manifests, and waits for the controller rollout without running `local-irsa` tasks. This path may share reusable units such as `kind:create`, `kind:context`, `cert-manager:install`, `manifests:apply`, `controller-image:build`, `controller-image:load-kind`, and `controller:rollout-status`, but it must not call AWS-specific tasks.

The controller Pod does not receive `CF_API_TOKEN` through environment variables. The controller reads the token only from the Kubernetes Secret referenced by `CloudflareIdentity`. Host-side `CF_ACCOUNT_ID` and `CF_API_TOKEN` are used by Taskfile, `flarectl`, and kest assertions.

`cloudflare:dev:zone-lifecycle` and `test:kest:cloudflare` create Cloudflare zones with the local test prefix. The initial Cloudflare local integration scenario covers Zone lifecycle:

1. create platform and application namespaces.
2. create a Kubernetes Secret with the Cloudflare API token in the platform namespace.
3. create `CloudflareIdentity` and wait for `Accepted=True` and `Ready=True`.
4. create a `ZoneClass` referencing that identity.
5. create a `Zone` with a generated domain name using the `dns-api-local-` prefix.
6. wait for `Zone Accepted=True` and `Programmed=True`.
7. read `Zone.status.provider.data.zone.id` and `Zone.status.nameServers`.
8. confirm through the Cloudflare API that the zone exists, belongs to `CF_ACCOUNT_ID`, has `type=full`, and has status `pending` or `active`.
9. delete the Kubernetes `Zone` and wait for it to disappear.
10. confirm through the Cloudflare API that the zone no longer exists.

The local zone lifecycle check does not query public DNS resolvers and does not require Cloudflare `zone.status=active`. `pending` is successful when the API-observed state matches the dns-api design.

When Cloudflare provider kest tests create zones, the zone names use a stable test prefix that identifies the environment and a root-domain shape accepted by Cloudflare:

- local: `dns-api-local-<yyyymmdd>-<hhmm>-<short-id>.com`
- CI: `dns-api-ci-<yyyymmdd>-<hhmm>-<short-id>.com`

For example, `dns-api-local-20260605-0520-7c12928.com` is a valid local test zone name. A local development account check on 2026-06-05 confirmed that Cloudflare accepts this root-domain shape and rejects subdomain-shaped zones such as `dns-api-local-20260605-0520-7c12928.appthrust.io` and unregistered reserved-TLD zones such as `dns-api-local-20260605-0520-7c12928.example`.

`short-id` is a lowercase ASCII identifier with at least 6 characters. It should be generated from the kest run ID, Git commit prefix, random base36 text, or a combination. If a generated name already exists in the selected Cloudflare account, the test regenerates `short-id` instead of adopting or deleting the existing zone.

Cloudflare cleanup tasks target only zones in the selected development or CI account and only zones with a dns-api test prefix. Cloudflare record tags are desired state rather than ownership metadata. Cleanup therefore uses account boundary, root-domain test-name prefix, and explicit delete confirmation as the deletion guard.

`cloudflare:cleanup-kest-zones` behavior:

- reads `CF_ACCOUNT_ID` and `CF_API_TOKEN` from the process environment.
- refuses to run if `CF_ACCOUNT_ID` is not one of the known local or CI Cloudflare development account IDs unless an explicit `ALLOW_UNKNOWN_ACCOUNT=true` override is supplied.
- chooses the default cleanup prefix from the account: `dns-api-local-` for the local development account and `dns-api-ci-` for the CI account.
- allows a narrower `PREFIX` only when it starts with the default cleanup prefix.
- lists only Cloudflare zones whose names start with `PREFIX` in the selected account.
- prints zone ID, name, status, and created time when available, but never prints token values, authorization headers, or 1Password-expanded secrets.
- defaults to list-only behavior.
- deletes zones only when `DELETE=true` or `DELETE=1` and `CONFIRM_PREFIX` exactly equals the effective `PREFIX`.
- deletes the matching Cloudflare zones through the Cloudflare Zone API. It does not delete individual DNS records outside zone deletion.
- after delete mode, re-lists the effective prefix and reports any remaining matching zones.

The cleanup task must not list all Cloudflare accounts, must not infer an account by token visibility, must not delete zones from the production Cloudflare account, and must not accept broad prefixes such as an empty string, `dns-api-`, `dns-api`, `*`, `.com`, or a real organization domain suffix. It also must not use Cloudflare zone tags or DNS record tags as deletion selectors in the initial design. If a test leaves DNS records but the test zone still exists, cleanup deletes the whole test zone. If the test zone no longer exists, cleanup does not search for records in other zones.

Initial Cloudflare kest checks verify Cloudflare API state for zone creation, provider-assigned name servers, and zone deletion. Cloudflare RecordSet checks are added to `test:kest:cloudflare` when the Cloudflare RecordSet controller is implemented. They use the same local or CI Cloudflare account, create records only inside test zones with the local or CI prefix, verify Cloudflare DNS Records API state, and delete the Kubernetes `RecordSet` resources before deleting the `Zone`. The checks do not query public resolvers and do not require the Cloudflare zone to become publicly delegated. If Cloudflare reports a newly created zone as pending because parent-zone NS delegation has not been configured, local and CI checks still assert the API-observed state. Production-like delegated activation checks are a separate later scenario.

## GitHub Actions

Controller CI is split into an always-on controller check workflow and provider-specific kest workflows. The initial provider-specific workflows are Route 53 kest and Cloudflare kest. The Headlamp plugin workflows remain separate from controller CI.

The always-on controller check workflow runs on pull requests and pushes to `main`. It must not require AWS credentials. It uses devbox and performs these checks:

1. Run `task generate`.
2. Verify generated API and Kubernetes manifest files are current with `git diff --exit-code -- pkg/go/api app/operator/config`.
3. Run `task test`.
4. Run `go vet ./...`.
5. Build the default Kubernetes manifests with `kustomize build app/operator/config/default`.
6. Build the controller image with `docker build`.

The always-on controller check workflow covers code generation, Go unit tests, controller-runtime fake-client or envtest-style tests, webhook validation tests, manifest rendering, and controller image buildability. It does not create hosted zones and does not call AWS APIs.

The Route 53 kest workflow is separate because it creates external Route 53 resources and depends on AWS credentials. It runs on pushes to `main` and on `workflow_dispatch`. It must not run as an untrusted pull request workflow. It has `permissions.contents=read` and `permissions.id-token=write`.

The Cloudflare kest workflow is separate because it creates external Cloudflare zones and records and depends on Cloudflare credentials. It runs on pushes to `main` and on `workflow_dispatch`. It must not run as an untrusted pull request workflow. It has `permissions.contents=read` and does not need GitHub OIDC unless another setup step explicitly requires it.

When GitHub Actions runs kest against Route 53, it does not use AWS IAM Identity Center SSO. AWS provides a GitHub Actions IAM role that can be assumed through `sts:AssumeRoleWithWebIdentity` from the GitHub OIDC provider. The trust policy is scoped to the target repository, branch, tag, or environment claims.

The GitHub Actions workflow has `permissions.id-token=write` and `permissions.contents=read`. It exchanges the GitHub OIDC token for short-lived AWS credentials with `aws-actions/configure-aws-credentials`. Long-lived access keys are not stored in GitHub Secrets.

When GitHub Actions runs the controller inside a kind cluster, the controller uses WebIdentity credentials through `local-irsa`. GitHub Actions itself first assumes a setup role through GitHub OIDC. That setup role prepares the local-irsa issuer, kind cluster, pod identity webhook, and controller ServiceAccount binding. The controller Pod then assumes the local-irsa controller role from its projected ServiceAccount token. The Route 53 kest workflow must not pass the GitHub Actions runner's AWS environment variables into the controller Pod as a Kubernetes Secret.

The Route 53 kest workflow does not use `task up` because `task up` starts Tilt for interactive development. GitHub Actions calls only `task ci:kest` after checkout, devbox setup, and AWS credential setup. `ci:kest` first runs `deps:install`, then `ci:kind:up`, then `test:kest:ci`, so the CI path is reproducible from a devbox shell with the same Taskfile entrypoint. `deps:install` runs `bun install --frozen-lockfile`.

The Cloudflare kest workflow also does not use `task up`. GitHub Actions calls a Cloudflare-specific CI entrypoint after checkout, devbox setup, and Cloudflare secret injection. The Cloudflare CI entrypoint runs `deps:install`, `cloudflare:smoke-check`, a Cloudflare-specific kind setup that does not run AWS or `local-irsa` tasks, then runs only Cloudflare kest tests. It must not require AWS credentials.

The Cloudflare kest workflow receives credentials through GitHub Actions secrets or environment variables named `CF_ACCOUNT_ID` and `CF_API_TOKEN`. These values correspond to the `cloudflare` section in the 1Password item `appthrust-dns-api-gha`. The workflow must not use the local development 1Password item and must not embed raw Cloudflare token values in repository files.

Before running Cloudflare kest, the workflow performs the same smoke check shape as local development:

```sh
flarectl --json zone list
```

This check runs after environment variables are mapped to `CF_ACCOUNT_ID` and `CF_API_TOKEN`. The expected result for an empty CI account is `[]`; a non-empty list is also valid if every listed zone belongs to accepted CI test state. Authentication or authorization failure stops the workflow before cluster setup.

`ci:kind:up` is built from the same reusable Taskfile units used by local development wherever possible: `kind:create`, `kind:context`, `local-irsa:init`, `local-irsa:install`, `local-irsa:bind-controller`, `cert-manager:install`, `manifests:apply`, controller image build/load, and controller rollout wait. `manifests:apply` applies CRDs first, waits until they are established, and then applies the full default manifest set so provider custom resources do not race with API discovery. CI-specific tasks only supply fixed CI variables such as kind config, local-irsa state root, issuer bucket, CI AWS account ID, and controller role name.

The CI local-irsa issuer bucket is fixed so ephemeral GitHub Actions runners can regenerate the same issuer URL without committing local state. The bucket name comes from `LOCAL_IRSA_BUCKET` in GitHub Secrets. CI tasks generate the ignored `kind.yaml` from local-irsa state before creating the kind cluster. CI tasks set `LOCAL_IRSA_STATE_ROOT=.task/local-irsa-ci` so local reproduction does not collide with a developer's normal `~/.local/share/local-irsa` state. The local-irsa state directory and `state.json` are not committed. CI does not run `local-irsa down --delete-bucket`; the issuer bucket and IAM OIDC provider are treated as persistent CI infrastructure.

Route 53 kest tests set `Route53Identity.spec.accountID` from `ROUTE53_ACCOUNT_ID`. GitHub Actions reads this value from GitHub Secrets. The repository workflow and Taskfile do not commit a real account ID default.

The GitHub Actions setup role and the controller runtime role are separate roles. The setup role is assumed by GitHub Actions through GitHub OIDC and needs permissions to manage the fixed local-irsa S3 issuer bucket, the IAM OIDC Provider, and the local-irsa controller role binding. It may also need Route 53 read permissions for kest assertions that call AWS APIs from the runner. The controller runtime role is `local-irsa-dns-api-ci-controller`; it is bound to the controller ServiceAccount by local-irsa and receives `arn:aws:iam::<account-id>:policy/dns-api-route53-controller`. The setup role must not be injected into the controller Pod.

The Route 53 kest workflow may expose a manual input that maps to `KEST_PRESERVE_ON_FAILURE=1` for debugging. The default is to let kest clean up Kubernetes resources. External Route 53 leftovers are cleaned up through the normal tag-based cleanup path described in the kest test policy.

On failure, the Route 53 kest workflow prints controller diagnostics: Pods in `dns-api-system`, the controller Deployment description, and recent controller logs. Diagnostics must not print AWS secret values or credential material.

On failure, the Cloudflare kest workflow prints the same Kubernetes controller diagnostics and Cloudflare cleanup hints. Diagnostics must not print `CF_API_TOKEN`, 1Password references that resolve to token values, request authorization headers, or raw Cloudflare API token values.

For multi-account checks in GitHub Actions, the workflow does not switch directly between separate GitHub OIDC roles for each account. It uses one base credential from GitHub OIDC or local-irsa, and `Route53Identity.spec.assumeRoleChain` assumes target-account roles.
