# Role Provider Service

## Why

Every service that makes an access decision needs the same answer: *which groups
does this person belong to?* And every service tends to answer it for itself — one
reads an LDAP directly, another keeps a hardcoded list of admins, a third parses a
CSV somebody exported last semester. The result is not just duplicated work but
duplicated *truth*: the same person is in a group according to one service and not
according to another, and nobody can say which is right.

Group membership is also more than a flat list. "All students of DHBW Mannheim" is
a rule, not an enumeration; "the department admins" is a group that contains other
groups. Services that model membership as a list of addresses either grow their own
half-implementation of that or ask people to maintain the same list twice.

So this service answers that one question for everybody, from sources that can be
kept current, and returns the answer in a form callers can use without
interpretation.

## What it does

A **Zanzibar-style tuple store** with a small HTTP API:

- **Groups** carry a token (`group:dept_cs_faculty`), a display name and a
  description — the last two matter because humans pick groups from search results.
- **Members** are users (`user:alice@example.edu`), other **groups** (so groups
  nest, and membership resolves transitively), or **patterns**
  (`*@student.example.edu`), which is how "all students" stays a rule instead of
  50 000 rows.
- **Token resolution** is the primary query: given an address, return every token
  that person holds — direct, inherited through nesting, and matched by pattern.
  That single list is what a consuming service checks its rules against.
- **Group search** over name, display name and description, so a UI can offer
  "who should manage this?" without exposing the whole directory.
- **Sync sources** import groups and memberships from **CSV** or **LDIF** files, on
  a schedule or on demand, and keep a per-source log with the last status. For
  LDIF, `dn_email_regexp` extracts the address from a distinguished name.

Everything is answered from an in-memory catalog that is refreshed periodically,
so resolution stays cheap even when it is on the hot path of every request.

## API

Everything is served under `/v1`, and the service publishes its own OpenAPI
description at **`GET /swagger.json`** — that spec is the reference, so it cannot
drift from the implementation the way a hand-written endpoint list does.

Four groups of operations exist: resolving a person's tokens (the query consumers
actually run), searching people and groups, editing groups and their members, and
managing sync sources including upload, trigger and log.

Authentication is by bearer token, with reads and writes separated: a token from
`API_TOKENS` may read, a token from `API_WRITE_TOKENS` may also write. Consumers
that only resolve tokens should be given a read token.

## Running it locally

**Prerequisites:** Go 1.24+, optionally
[air](https://github.com/air-verse/air) for live reload.

```bash
make dev     # live-reload server on :8085, in-memory store
make run     # build + run once
make test
make all     # docs + binary
```

With `DB_TYPE=memory` and `DB_ADD_MOCK_DATA=true` the service starts with a small
set of example groups and identities — enough to develop a consumer against
without a database or a directory export. The deployment repo's
`run-development.sh` starts it this way alongside the other services.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `API_MODE` | `production` | `development` enables verbose logging |
| `API_BIND` | `:8085` | Listen address |
| `API_TOKENS` | — | Comma-separated bearer tokens allowed to read |
| `API_WRITE_TOKENS` | — | Comma-separated bearer tokens allowed to write |
| `CORS_ALLOWED_ORIGINS` | — | Origins for browser access |
| `DB_TYPE` | `memory` | `memory` \| `postgres` |
| `DB_CONNECTION_STRING` | local Postgres DSN | Used when `DB_TYPE=postgres` |
| `DB_ADD_MOCK_DATA` | `false` | Seed example groups and identities |
| `GROUP_CACHE_REFRESH_SECONDS` | `600` | How often the in-memory catalog is rebuilt |
| `MAX_RESPONSE_LIMIT` | `50` | Cap on rows per search response |
| `SERVICE_TIMEOUT_SECONDS` | `30` | Per-request timeout |

`memory` loses everything on restart and is for development only; production runs
`postgres`.

## Consumers

[openstack-management-api](https://github.com/pfisterer/openstack-management-api)
uses it with `ROLE_PROVIDER=http`, `ROLE_PROVIDER_URL` and a read token: it
resolves the caller's tokens on every request and searches groups when someone
picks who may manage a budget. The DHBW installation keeps it **internal to the
cluster** — no ingress — because it answers questions about people and has no
business being reachable from outside.

## Deployment

A Helm chart lives in [`helm-chart/`](helm-chart) (deployment + secret, with a
`values.schema.json` that fails a bad values file at install time instead of at
runtime). It is deployed via ArgoCD from the `dhbw-deployment` repo. Images go to
`ghcr.io/pfisterer/role-provider-service`; `X.Y.Z-test.N` is the staging channel,
plain semver production.

The chart is published as an OCI artifact on every push to `main`:

```sh
helm pull oci://ghcr.io/pfisterer/charts/role-provider-service --version 0.6.5-test.1
```

It is normally not installed on its own. The DHBW deployment composes all four
services with the [cloud-self-service](https://github.com/pfisterer/cloud-self-service)
umbrella chart, which pins this chart by version — and a pinned chart version
pins its `appVersion`, which pins the image tag. Values for this chart go under
its chart name there:

```yaml
role-provider-service:
  roleProviderService:
    ...
```

## Related projects

- [cloud-self-service](https://github.com/pfisterer/cloud-self-service) — the umbrella chart that composes all four
- [openstack-management-api](https://github.com/pfisterer/openstack-management-api) — first consumer
- [self-service-ui](https://github.com/pfisterer/self-service-ui) — where group search surfaces
- [dynamic-zones](https://github.com/pfisterer/dynamic-zones) — DNS self-service

## License

See [LICENSE](./LICENSE).
