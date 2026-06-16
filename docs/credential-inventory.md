# Credential Inventory

This file tracks environment-specific values that must move out of committed files before making this repository public.

## Naming Policy

Use the same variable names for local and CI when the setting means the same thing. The value source decides the environment:

- `.env.dev`: committed local development template with `op://` references. Copy it to ignored `.env` before local use.
- `.env`: ignored local development values, normally copied from `.env.dev`.
- `.env.gha`: committed GitHub Actions secret-setting template with `op://` references.
- GitHub Secrets: CI runtime values.

Variables that do not change by environment are excluded from this inventory.

## Environment Files

`.env.dev` and `.env.gha` are committed because they contain only 1Password references and non-secret defaults. Raw secret values must not be committed.

`.env` is ignored by git and is for values used by local development commands. Create it by copying `.env.dev`:

```sh
cp .env.dev .env
```

Local commands can be run through `op run` or `opx` so `op://` references resolve in the local shell.

`.env.gha` is for values that are copied into GitHub Actions Secrets from a local machine. GitHub Actions does not run `op run` and does not read `.env.gha` at runtime. After the values are set as GitHub Secrets, workflows read only the GitHub Secrets.

Set repository secrets with:

```sh
op run --env-file .env.gha -- task github:secrets:set
```

Use the same variable names in `.env`, `.env.gha`, and GitHub Secrets when the meaning is the same. Do not add `CI_` only to show that a value is used by GitHub Actions.

Recommended file split:

| File or store | Purpose | Example variables |
| --- | --- | --- |
| `.env.dev` | Committed template for local development and local e2e values. | `AWS_PROFILE`, `ROUTE53_ACCOUNT_ID`, `CF_ACCOUNT_ID`, `CF_API_TOKEN`, `CLOUDFLARE_ZONE_NAME_PREFIX` |
| `.env` | Ignored local copy used by local commands. | `AWS_PROFILE`, `ROUTE53_ACCOUNT_ID`, `CF_ACCOUNT_ID`, `CF_API_TOKEN`, `CLOUDFLARE_ZONE_NAME_PREFIX` |
| `.env.gha` | Source file for setting GitHub Actions Secrets from a local machine. | `AWS_ROLE_TO_ASSUME`, `ROUTE53_ACCOUNT_ID`, `LOCAL_IRSA_BUCKET`, `CONTROLLER_POLICY_ARN`, `CF_ACCOUNT_ID`, `CF_API_TOKEN` |
| GitHub Secrets | Runtime values used by GitHub Actions. | `AWS_ROLE_TO_ASSUME`, `ROUTE53_ACCOUNT_ID`, `LOCAL_IRSA_BUCKET`, `CONTROLLER_POLICY_ARN`, `CF_ACCOUNT_ID`, `CF_API_TOKEN` |

`AWS_PROFILE` may appear in `.env.gha` only for local inspection or secret-setting tasks. `CLOUDFLARE_ZONE_NAME_PREFIX` may appear in `.env.gha` only when a local cleanup command needs to target CI-shaped Cloudflare test zones. GitHub Actions itself should not depend on `AWS_PROFILE`; it uses GitHub OIDC and `AWS_ROLE_TO_ASSUME`.

1Password uses two items:

- `appthrust-dns-api-dev`: values for `.env`.
- `appthrust-dns-api-gha`: values for `.env.gha` and GitHub Actions Secrets.

Each item groups fields by section. Use `route53` for AWS, Route 53, local-irsa, and controller policy values used by the Route 53 path. Use `cloudflare` for Cloudflare account and API token values.

## Target Variables

| Variable | Env file | Value | Vault | Item | Section | Field | Used by |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `AWS_PROFILE` | `.env.dev` | `appthrust-dns-api-dev`. | None | None | None | None | Local |
| `AWS_PROFILE` | `.env.gha` | `appthrust-dns-api-ci`. Empty in GitHub Actions because GitHub Actions uses OIDC. | None | None | None | None | CI |
| `AWS_ROLE_TO_ASSUME` | `.env.gha` | `arn:aws:iam::<account-id>:role/dns-api-github-actions-ci`. | `Private` | `appthrust-dns-api-gha` | `route53` | `role-to-assume` | CI |
| `ROUTE53_ACCOUNT_ID` | `.env.dev` | Example: `123456789012`. | `Private` | `appthrust-dns-api-dev` | `route53` | `account-id` | Local |
| `ROUTE53_ACCOUNT_ID` | `.env.gha` | Example: `210987654321`. | `Private` | `appthrust-dns-api-gha` | `route53` | `account-id` | CI |
| `LOCAL_IRSA_BUCKET` | `.env.gha` | Example: `local-irsa-<account-id>-<region>-dns-api-ci-<suffix>`. | `Private` | `appthrust-dns-api-gha` | `route53` | `local-irsa-bucket` | CI |
| `CONTROLLER_POLICY_ARN` | `.env.gha` | Example: `arn:aws:iam::<account-id>:policy/dns-api-route53-controller`. | `Private` | `appthrust-dns-api-gha` | `route53` | `controller-policy-arn` | CI |
| `CF_ACCOUNT_ID` | `.env.dev` | `023e105f4ecef8ad9ca31a8372d0c353`. | `Private` | `appthrust-dns-api-dev` | `cloudflare` | `account-id` | Local |
| `CF_ACCOUNT_ID` | `.env.gha` | `023e105f4ecef8ad9ca31a8372d0c354`. | `Private` | `appthrust-dns-api-gha` | `cloudflare` | `account-id` | CI |
| `CF_API_TOKEN` | `.env.dev` | Secret value. | `Private` | `appthrust-dns-api-dev` | `cloudflare` | `api-token` | Local |
| `CF_API_TOKEN` | `.env.gha` | Secret value. | `Private` | `appthrust-dns-api-gha` | `cloudflare` | `api-token` | CI |
| `CLOUDFLARE_ZONE_NAME_PREFIX` | `.env.dev` | `dns-api-local-`. | None | None | None | None | Local |
| `CLOUDFLARE_ZONE_NAME_PREFIX` | `.env.gha` | `dns-api-ci-`. | None | None | None | None | CI |

## Variable Purposes

- `AWS_PROFILE`: AWS shared config profile for local Route 53 e2e and local inspection of CI resources.
- `AWS_ROLE_TO_ASSUME`: GitHub Actions setup role ARN for `appthrust/dns-api`, used by `aws-actions/configure-aws-credentials` in `route53-ci`.
- `ROUTE53_ACCOUNT_ID`: Expected AWS account ID for `Route53Identity.spec.accountID` and Route 53 e2e assertions.
- `LOCAL_IRSA_BUCKET`: S3 bucket used by local-irsa as the OIDC issuer.
- `CONTROLLER_POLICY_ARN`: IAM policy ARN attached to the local-irsa controller role.
- `CF_ACCOUNT_ID`: Cloudflare account ID for Cloudflare e2e and cleanup tasks.
- `CF_API_TOKEN`: Cloudflare API token for Cloudflare identity checks, e2e tests, and cleanup tasks.
- `CLOUDFLARE_ZONE_NAME_PREFIX`: Zone domain-name prefix used by cleanup tasks to limit deletion to test zones.

## Current Names To Remove Or Rename

| Current variable | Replacement | Reason |
| --- | --- | --- |
| `PROFILE` | `AWS_PROFILE` | Use the standard AWS SDK / CLI environment variable name. |
| `CI_AWS_PROFILE` | `AWS_PROFILE` | The profile is an AWS profile, not a CI resource. CI/local separation belongs in `.env` vs `.env.gha`. |
| `CI_AWS_ACCOUNT_ID` | `ROUTE53_ACCOUNT_ID` | Same setting as Route 53 account ID. |
| `CI_KIND_CONFIG` | Remove | Use the ignored `kind.yaml` path for both local and CI. The file contents differ by environment and are generated or prepared before `kind:create`. |
| `CI_LOCAL_IRSA_BUCKET` | `LOCAL_IRSA_BUCKET` | Same local-irsa concept; CI value should come from GitHub Secrets. |
| `CI_CONTROLLER_ROLE_NAME` | `CONTROLLER_ROLE_NAME` | Same controller role concept; CI value should come from GitHub Secrets. |
| `CI_CONTROLLER_POLICY_ARN` | `CONTROLLER_POLICY_ARN` | Same controller policy concept; CI value should come from GitHub Secrets. |
| `CLOUDFLARE_CI_ACCOUNT_ID` | `CF_ACCOUNT_ID` | Same Cloudflare account ID concept; CI value should come from GitHub Secrets. |
| `CLOUDFLARE_CI_API_TOKEN` | `CF_API_TOKEN` | Same Cloudflare token concept; CI value should come from GitHub Secrets. |
| `CLOUDFLARE_CLEANUP_ACCOUNT_IDS` | Remove | Cleanup should use the selected `CF_ACCOUNT_ID` plus `CLOUDFLARE_ZONE_NAME_PREFIX`, not a shared account allowlist. |

## Excluded Stable Values

| Variable | Value | Reason |
| --- | --- | --- |
| `AWS_REGION` | `ap-northeast-1` | The project currently uses the same AWS region for local and CI. |
| `KIND_CLUSTER` | Local: `dns-api`; CI: `dns-api-ci` | Execution resource name. It is not a credential or account setting. |
| `LOCAL_IRSA_NAME` | Local defaults to `KIND_CLUSTER`; CI currently uses `dns-api-ci` | Execution resource name. It is not a credential or account setting. |
| `LOCAL_IRSA_STATE_ROOT` | CI currently uses `.task/local-irsa-ci` | Local state path under ignored `.task/`. It is not a credential or account setting. |
| `CONTROLLER_ROLE_NAME` | CI currently uses `local-irsa-dns-api-ci-controller` | Role name alone is not enough to grant access. It can remain configurable but is not a public-release blocker. |
| `KEST_PRESERVE_ON_FAILURE` | `1` or empty | Debug control flag, not an environment-specific credential or account setting. |

## Notes

- `.env.dev` should contain local development `op://` references only.
- `.env.gha` should contain CI `op://` references only and is used only to set GitHub Actions Secrets from a local machine.
- `.env` should be ignored and created from `.env.dev`.
- GitHub Actions should read CI values from GitHub Secrets, not from committed defaults.
- GitHub Secrets cannot be read back. If a value is not present in design docs, provider consoles, or 1Password, it is not recoverable from GitHub.
- The old `suin/dns-api` repository used `arn:aws:iam::<account-id>:role/dns-api-github-actions-ci-suin` for `AWS_ROLE_TO_ASSUME`. The public `appthrust/dns-api` repository should use `arn:aws:iam::<account-id>:role/dns-api-github-actions-ci`.
