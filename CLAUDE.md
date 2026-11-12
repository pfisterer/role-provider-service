# role-provider-service — notes for Claude Code

Read `README.md` first. This file lists the rules that are easy to get wrong.

## Go client (`client/`)

- `client/` is the Go module `github.com/pfisterer/role-provider-service/client`, which openstack-management-api and aiaas-management-api import. Its `client.gen.go` is generated: never edit it by hand, never generate a client in a consumer.
- API changed (any swag annotation or request/response type under `internal/webserver`, `internal/common`, `internal/groupmgmt`) → `make client`, commit `client/` with the change, tag `client/vX.Y.Z` on that commit and push the tag (`git push origin client/vX.Y.Z`); then the consumers bump with `go get github.com/pfisterer/role-provider-service/client@vX.Y.Z`. Without the tag the consumers cannot see the change. CI job `go-client` fails when `client/` is stale.
- Client tags are independent of the service version: a client tag neither bumps `VERSION` nor releases the service, and a service release does not need a client tag unless the API changed.
- The root module's `./...` stops at `client/go.mod`: check the client module separately (`cd client && go build ./... && go vet ./... && go test ./...`).

## Conventions

- Commits: `area: lowercase summary`, no co-author or other attribution lines.
- Comments explain why; no hard-wrapped comments, strings or Markdown.
