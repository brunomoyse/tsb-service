# Backend contract with the clients

What tsb-service (pili-api) promises to the apps that call it, as two committed files generated from the code:

| File | What it is | Used by |
|---|---|---|
| `contract/schema.graphql` | The complete SDL of the executable schema (types, enums, directives, scalars, all `schema/*.graphql` merged by gqlgen). | pili-shop, pili-dashboard, pili-mobile (operation validation); pili-handheld (Apollo schema copy); graphql-inspector (breaking-change diff). |
| `contract/mcp-tools.json` | Name and argument JSON schema of every tsb-mcp tool (descriptions left out on purpose). | pili-agent (`internal/stubmcp` contract test). |

Both are produced by `go run ./cmd/contract` (code in `internal/contract`). `go run ./cmd/contract -check` fails when a file is stale; `go test ./internal/contract` does the same, so a normal test run catches a forgotten update.

## Changing the schema

1. Edit `internal/api/graphql/schema/*.graphql`.
2. `go run github.com/99designs/gqlgen generate`
3. `go run ./cmd/contract`
4. Commit the schema, `generated.go`, the resolver stubs and `contract/` together.

Order of work between repositories:

- Additive change (new field, new optional argument): merge and deploy the backend first, then the client that uses it (a client PR fails while its field is missing from pili-api main).
- Removal or tightening (field removed, nullable became non-null, new required argument, enum value removed): remove the usage from the clients first and release them, then change the backend. The `deployed-clients` job fails otherwise.
- A deliberate break that cannot be sequenced: label the PR `breaking-schema-change` (see below).

## What CI checks (`.github/workflows/graphql-contract.yml`)

All jobs run on GitHub-hosted runners (this repository is public). They start on pull requests and pushes to main that touch the GraphQL, MCP or contract files.

| Job | Fails when |
|---|---|
| `generated-code` | `gqlgen generate` changes any tracked file, or `go run ./cmd/contract -check` finds `contract/` stale. |
| `breaking-change` | `graphql-inspector diff` between the schema before the PR (first parent of the merge commit) and the PR's finds a breaking change. Adding fields, types, enum values or optional arguments is allowed. |
| `deployed-clients` | The operations in `main` of pili-shop or pili-dashboard no longer validate against the PR's schema (unknown field or argument, wrong variable type, missing selection). Uses each client's own `scripts/check-graphql-operations.mjs`; while a client has not merged that script yet the job only warns. |

Escape hatch: add the label `breaking-schema-change` to the PR. `breaking-change` and `deployed-clients` then print a warning instead of failing. Use it only when the clients are already compatible, or are released together with the change, and say why in the PR description. The label is read when the job runs: adding it re-runs the workflow.

## What the clients check

Each client downloads `contract/schema.graphql` from `https://raw.githubusercontent.com/nuagemagique-dev/pili-api/main/contract/schema.graphql` and fails on a mismatch:

- pili-shop and pili-dashboard (`scripts/check-graphql-operations.mjs`, workflow `graphql-contract.yml`): pull requests, pushes to main and daily. The script finds every operation (a string literal preceded by `/* GraphQL */`, or a `gql` / `graphql` tagged template; shared selections such as `${ORDER_ITEMS_SELECTION}` and small helpers such as `configFields(true)` are evaluated), validates it with graphql-js, and fails on any other string literal that looks like an operation, so a new query cannot skip the check. Run it locally with `npm run check:graphql` (reads `../tsb-service/contract/schema.graphql`, or `GRAPHQL_SCHEMA=<file>`).
- pili-mobile: same script, pull requests touching `src/` and weekly.
- pili-handheld: the Apollo schema is a committed copy (`schema.graphqls`). The workflow fails when it differs from pili-api main or when an operation no longer validates. Refresh with `./gradlew :app:copySchemaFromService`.
- pili-agent: `internal/stubmcp` is compared with `contract/mcp-tools.json` (tool exists, argument names and types, required arguments, hidden tools still exist). Pull requests touching the stub and weekly.

## User actions

- Merge order: pili-api first (it publishes `contract/`), then the clients. Until the pili-api branch is on main, the client workflows cannot download the schema and fail.
- Branch protection: mark `Generated code is up to date`, `No breaking schema change` and `Public clients still validate (pili-shop)` / `(pili-dashboard)` as required checks on pili-api main, and each client's `Operations match the backend schema` (pili-shop, pili-dashboard, pili-mobile), `Schema copy is current and operations validate` (pili-handheld) and `Stub matches tsb-mcp` (pili-agent). The workflows are path-filtered, so a required check that never runs would block unrelated PRs: either keep the checks optional, or remove the `paths:` filters from the workflows you make required.
- Create the label `breaking-schema-change` in pili-api.
- Private clients are not checked from this repository (no secrets were added). To guard pili-mobile and pili-handheld before merge, create a read-only token (fine-grained PAT or GitHub App) with Contents: read on those two repositories, store it as the pili-api secret `CLIENTS_READ_TOKEN`, and add matrix entries to `deployed-clients` with `token: ${{ secrets.CLIENTS_READ_TOKEN }}` on the checkout (mobile: `--root src`; handheld: run `.github/ci/validate-operations.mjs` on `app/src/main/graphql/.../operations`). Until then they are guarded by their own workflows, which run after the backend change.
- A schema change that is additive for pili-handheld still fails its drift check until the copy is refreshed (by design: the committed copy must equal pili-api main). Dependabot PRs in pili-handheld only run the check when they touch its GraphQL files.
