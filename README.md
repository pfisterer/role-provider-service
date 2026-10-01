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
- **Relations** say what a member is in a group, beyond being in it: a course
  `wwi23seb` can have a `dozent` and `studierende`. The relations are configured
  (`GROUP_RELATIONS`), every one implies membership, and a user holding one gets
  `group:wwi23seb#dozent` in addition to `group:wwi23seb` — so a rule on the
  plain group token keeps meaning "everyone in the course". A group as member of
  `wwi23seb#studierende` gives the relation to all of that group's members.
- **Token resolution** is the primary query: given an address, return every token
  that person holds — direct, inherited through nesting, and matched by pattern.
  That single list is what a consuming service checks its rules against.
- **Group search** over name, display name and description, so a UI can offer
  "who should manage this?" without exposing the whole directory.
- **User search** by email address only (case-insensitive substring). The store holds no names, so nobody can be looked up by name, and people who are members only through a pattern have no row and are not found.
- **Sync sources** import groups and memberships from **CSV** or **LDIF** files — uploaded through the API, or read from a `file_path` on demand or on a cron `schedule` — and keep a per-source log with the last status. A sync replaces every membership that source owns, so a source is always exactly its latest file.
- **A Keycloak source** derives groups from the attributes the identity provider holds about everyone who has signed in, e.g. the bwIDM affiliation — nothing to import by hand (see below).
- **One spelling for every id.** Addresses, group ids and patterns are stored and looked up in lowercase, so `A.B@x` and `a.b@x` are one person; rows written before that are rewritten at startup.

**Group search** reads the query the way search boxes usually do: words are alternatives (`mannheim karlsruhe`), a quoted phrase is required (`"fak technik"`), `UND`, `AND`, `&` or a leading `+` make terms required (`mannheim UND studierende`), and `*` is a wildcard matched against a whole field or word (`stud*`, `*-ma`). Terms compare case-insensitively with umlauts spelled out, so `beschäftigte` finds `beschaeftigte`, and a `group:` prefix is ignored. Besides the groups themselves, every relation someone holds in a group is offered as an entry of its own (`group:standort-ma#beschaeftigte`, described as the group plus `· Rolle: beschaeftigte`), with the relation's name as a search term. An exact ID comes first, then entries matching more terms.

**A group belongs to one writer**: the source that created it, or the API. A sync that would write members into a group owned by another source fails, naming the owner, and the API refuses to change members of a group a source maintains. Two sources filling the same name would silently merge two meanings; the remedy is a prefix of the source's own (the Keycloak groups are `standort-*`), and combining sources on purpose works by referring to the other source's group as a member.

Group search is answered from an in-memory snapshot of the group catalog, reloaded on startup, after every sync and after every group edit, with `GROUP_CACHE_REFRESH_SECONDS` only as a backstop — so a type-ahead never costs a database round-trip. Token resolution queries the store directly (a recursive query on Postgres), so a membership change counts on the next request.

## Import formats

A **CSV** source has one membership per row: `group,member[,description]`. If the first row's first column is `group` or `group_id`, it is a header: it names the columns (`group`, `member`, `description`, `relation`, and `name`/`note` for text the import ignores; any other name is an error) and is skipped. A byte-order mark in front of it, as Excel writes one, is ignored. A `relation` column gives each row's relation; rows without one, and files without the column, are plain memberships. A relation that is not configured fails the whole sync, with the reason in the sync log; lines starting with `#` are comments, rows with fewer than two columns are ignored, and a `group:` prefix in the first column is dropped. The member is `user:<email>`, `group:<name>` or `pattern:<glob>` (`*` matches any run of characters, case-insensitive); a bare value counts as a user if it contains `@` and as a group otherwise. The optional description belongs to the group, so the last non-empty one wins.

```csv
group,member,description,relation
dept_cs_faculty,alice@example.edu,CS faculty members,
dept_cs_admin,group:dept_cs_faculty,,
students,pattern:*@student.example.edu,All students,
wwi23seb,bob@example.edu,Course WWI23SEB,dozent
wwi23seb,group:wwi23seb-kurs,,studierende
```

An **LDIF** source reads entries whose `objectClass` is `groupOfNames`, `groupOfUniqueNames`, `posixGroup` or `group`. The group name is the first `cn` of the entry's DN, its `description` attribute becomes the group description, and each `member`/`uniqueMember` DN is turned into an address by the source's `dn_email_regexp` (required, first capture group, e.g. `mail=([^,]+)`). DNs the expression does not match are skipped. An optional `group_relation_regexp` turns LDAP groups into relations of another group: it is matched against the CN, and its named captures `group` and `relation` give the target — `^(?P<group>.+)-(?P<relation>dozent)$` makes `cn=wwi23seb-dozent` the `dozent` relation of `wwi23seb`. A CN it does not match stays a plain group, and a relation group's description is not applied to the target group.

### Keycloak

With `KEYCLOAK_REALM_URL` set, the service keeps one source of type `keycloak`, created at startup and read on `KEYCLOAK_SYNC_SCHEDULE` and once right after the start. It reads every enabled user through the admin API with a service account that needs `realm-management/view-users` and nothing else, and turns the values of one attribute, each of the form `<value>@<scope>`, into memberships by the rules in `KEYCLOAK_MAPPING`:

```json
{
  "attribute": "edu_person_affiliation",
  "all_group": {"group": "dhbw", "description": "All locations"},
  "locations": {"dhbw-mannheim.de": {"group": "standort-ma", "description": "DHBW Mannheim"}},
  "roles": {"student": "studierende", "staff": "beschaeftigte", "employee": "beschaeftigte", "faculty": "lehrende"},
  "ignored_scopes": ["kit.edu"],
  "ignored_values": ["member", "affiliate"]
}
```

The scope picks the location's group, the value the relation held there; a user with a known scope but no mapped value is a plain member. `all_group` receives the same relations across all locations. The two tables are independent, so a combination nobody had yet needs no entry. Nothing is guessed: a scope no location names and a value neither mapped nor ignored grant nothing beyond the location, and are listed with their counts in the run's `notes` in the sync log. Every relation under `roles` must be in `GROUP_RELATIONS`, or the service refuses to start.

Keycloak creates a user at their first sign-in, and that sign-in asks for their tokens. So a token request for an address the last run did not know reads that one user (at most once per ten minutes per address, with a three-second timeout) and adds their memberships under the Keycloak source, which the next run then owns.

## API

Everything is served under `/v1`, and the service publishes its own OpenAPI
description at **`GET /swagger.json`** — that spec is the reference, so it cannot
drift from the implementation the way a hand-written endpoint list does.

Four groups of operations exist: resolving a person's tokens (the query consumers actually run), searching people and groups, editing groups and their members, and managing sync sources (`/v1/sync/sources`) including upload, trigger and log. Next to them, `/v1/admin/health` and `/v1/admin/stats` report liveness and, on Postgres, the number of groups, tuples and sources. Every stable GitHub release carries its `swagger.json` as an asset, so a consumer can generate its client against a pinned version rather than a running instance.

Authentication is by bearer token on everything under `/v1` (only `/` and `/swagger.json` are open), with reads and writes separated: a token from `API_TOKENS` may read, a token from `API_WRITE_TOKENS` may also write, and a read token on a write operation gets `403`. The write list has no fallback — left empty, nobody can write — because whoever writes the group graph writes authorization for every consumer. Consumers that only resolve tokens should be given a read token.

## Running it locally

**Prerequisites:** Go 1.25+, optionally [air](https://github.com/air-verse/air) for live reload. `make generate-docs` installs [swag](https://github.com/swaggo/swag) if it is missing.

```bash
make all     # docs + binary — run once after cloning, the embedded docs are generated
make dev     # live-reload server on :8085, in-memory store
make run     # build + run once
make test
```

The service refuses to start without `API_TOKENS`. Configuration comes from the environment and from a `.env` file in the working directory, whose values override variables already set.

With `DB_TYPE=memory` and `DB_ADD_MOCK_DATA=true` the service starts with a small set of example groups and identities — the same ones the openstack-management-api mock data uses — enough to develop a consumer against without a database or a directory export.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `API_MODE` | `production` | `development` enables debug logging (including response bodies), disables HTTP caching and additionally allows loopback CORS origins |
| `API_BIND` | `:8085` | Listen address |
| `API_TOKENS` | — (required) | Comma-separated bearer tokens allowed to read |
| `API_WRITE_TOKENS` | — | Comma-separated bearer tokens allowed to write; empty means nobody can write |
| `CORS_ALLOWED_ORIGINS` | — | Exact origins allowed for browser access; empty allows none |
| `DB_TYPE` | `memory` | `memory` \| `postgres` |
| `DB_CONNECTION_STRING` | local Postgres DSN | Used when `DB_TYPE=postgres` |
| `DB_ADD_MOCK_DATA` | `false` | Seed example groups and identities |
| `GROUP_CACHE_REFRESH_SECONDS` | `600` | Backstop interval for reloading the group search snapshot; `<= 0` disables it |
| `GROUP_RELATIONS` | *(empty)* | Relations a group can carry besides `member`, comma-separated (e.g. `dozent,studierende`); names are lowercase letters, digits, `-`, `_`. `GET /v1/relations` lists them |
| `KEYCLOAK_REALM_URL` | *(empty)* | Realm issuer URL (`https://…/realms/<name>`); empty disables the Keycloak source |
| `KEYCLOAK_CLIENT_ID` / `KEYCLOAK_CLIENT_SECRET` | — | Service account with `realm-management/view-users` |
| `KEYCLOAK_MAPPING` | — | JSON rules turning attribute values into groups (see Import formats) |
| `KEYCLOAK_SYNC_SCHEDULE` | `*/15 * * * *` | Cron schedule of the full read |
| `MAX_RESPONSE_LIMIT` | `50` | Cap on results per group or user search |
| `SERVICE_TIMEOUT_SECONDS` | `30` | Per-request timeout |

`memory` loses everything on restart and is for development only; production runs
`postgres`.

## Consumers

[openstack-management-api](https://github.com/pfisterer/openstack-management-api) uses it with `ROLE_PROVIDER=http`, `ROLE_PROVIDER_URL` and a read token in `ROLE_PROVIDER_API_TOKEN`: it resolves the caller's tokens, searches groups and addresses when someone picks who may manage something, and its OpenStack reconciler reads a group's resolved members to keep the matching Keystone group in step. Keep it **internal to the cluster** — no ingress — because it answers questions about people and has no business being reachable from outside. The chart creates an Ingress by default; `ingress.enabled: false` turns it off.

## Deployment

**Normally deployed as part of [cloud-self-service](https://github.com/pfisterer/cloud-self-service)**, the umbrella chart that composes this service with the other three and pins it by version — and a pinned chart version pins its `appVersion`, which pins the image tag. Installing this chart on its own works, but then nothing keeps it in step with the services it talks to.

A Helm chart lives in [`helm-chart/`](helm-chart) (deployment, service, optional ingress and secret, with a `values.schema.json` that fails a bad values file at install time instead of at runtime). Instead of letting the chart create the secret, `roleProviderService.existingSecret` can name one that provides the keys `db-connection-string`, `api-tokens` and, optionally, `api-write-tokens`. Images go to `ghcr.io/pfisterer/role-provider-service`; `X.Y.Z-test.N` is the staging channel, plain semver production.

The chart is published as an OCI artifact on every push to `main` whose version is not published yet — neither a chart nor an image version is ever overwritten. Stable versions additionally get a Git tag and a GitHub release.

```sh
helm pull oci://ghcr.io/pfisterer/charts/role-provider-service --version 0.6.8
```

Values for this chart go under its chart name in the umbrella:

```yaml
role-provider-service:
  roleProviderService:
    ...
```

## Related projects

- [cloud-self-service](https://github.com/pfisterer/cloud-self-service) — the umbrella chart that composes all four
- [openstack-management-api](https://github.com/pfisterer/openstack-management-api) — first consumer
- [cloud-self-service-golib](https://github.com/pfisterer/cloud-self-service-golib) — shared Go library (configuration, logging, CORS)
- [self-service-ui](https://github.com/pfisterer/self-service-ui) — where group search surfaces
- [dynamic-zones](https://github.com/pfisterer/dynamic-zones) — DNS self-service

## License

See [LICENSE](./LICENSE).
