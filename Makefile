PROJECT_NAME := role-provider-service
BINARY_NAME  := $(PROJECT_NAME)
SRC_DIR      := ./cmd
DOC_DIR      := ./internal/generated_docs
BUILD_DIR    := ./tmp/build
GO_MOD       := go.mod

SWAGGER_JSON := $(DOC_DIR)/swagger.json
EMBED_FILE   := $(DOC_DIR)/embedded.go
OPENAPI_JSON := $(DOC_DIR)/openapi3.json
CLIENT_DIR   := $(DOC_DIR)/client-typescript
CLIENT_PKG_DIR := ./client-npm
NPM_PKG_NAME := @dhbw-cloud/role-provider-client

# Docker image details
DOCKER_REPO      ?= ghcr.io/pfisterer/$(PROJECT_NAME)
DOCKER_TAG       ?= $(shell cat VERSION 2>/dev/null || echo "dev")
DOCKER_PLATFORMS ?= linux/amd64,linux/arm64

.DEFAULT_GOAL := all

.PHONY: publish all build clean doc check-swag generate-docs run dev test tidy lint help \
        docker-build docker-run docker-multi-arch-build helm-update bump version-check \
        install-npm client npm-package npm-pack npm-publish

all: generate-docs build

# ── Documentation ─────────────────────────────────────────────────────────────

# Ensure swag is installed
check-swag:
	@command -v swag >/dev/null 2>&1 || go install github.com/swaggo/swag/cmd/swag@latest

# Generate swagger.json from inline annotations and write embedded.go
generate-docs: check-swag
	@echo "📚 Generating swagger.json..."
	@mkdir -p $(DOC_DIR)
	@set -e; swag init -g doc.go --dir $(SRC_DIR),./internal/webserver,./internal/common,./internal/groupmgmt -o $(DOC_DIR) --outputTypes json
	@echo "🧩 Copying VERSION file to $(DOC_DIR)..."
	@cp VERSION $(DOC_DIR)/VERSION
	@echo "🧩 Writing $(EMBED_FILE)..."
	@printf '%s\n' \
		'package generated_docs' \
		'' \
		'import _ "embed"' \
		'' \
		'//go:embed swagger.json' \
		'var SwaggerJSON string' \
		'//go:embed VERSION' \
		'var Version string' \
		> $(EMBED_FILE)
	@echo "✅ Docs generated in $(DOC_DIR)/"

# Alias
doc: generate-docs

# ── TypeScript client ─────────────────────────────────────────────────────────
# The self-service UI maintains groups through /v1/manage and calls it only through this generated client, taken as an npm package at build time: an operation the deployed API lacks is then a build error there, not a silent no-op in the browser. Same tooling and layout as openstack-management-api's client.

install-npm:
	@npm install --silent

client: generate-docs install-npm
	@echo "🔁 Converting Swagger 2 → OpenAPI 3..."
	@npx swagger2openapi $(SWAGGER_JSON) --outfile $(OPENAPI_JSON) --yaml=false --patch --warnOnly
	@echo "📦 Generating TypeScript client..."
	@rm -rf $(CLIENT_DIR) && mkdir -p $(CLIENT_DIR)
	@npx openapi-ts -i "file://$(abspath $(OPENAPI_JSON))" -o "$(CLIENT_DIR)" -c @hey-api/client-fetch
	@rm -f $(OPENAPI_JSON)
	@echo "✅ TS client generated in $(CLIENT_DIR)"

npm-package: client
	@echo "📦 Assembling $(NPM_PKG_NAME)..."
	@rm -rf $(CLIENT_PKG_DIR)
	@mkdir -p $(CLIENT_PKG_DIR)/src
	@cp -R $(CLIENT_DIR)/. $(CLIENT_PKG_DIR)/src/
	@# The generated index.ts exports every operation and type, but not the client instance the consumer configures.
	@printf '%s\n' \
		"export * from './index';" \
		"export { client } from './client.gen';" \
		> $(CLIENT_PKG_DIR)/src/entry.ts
	@VERSION=$$(cat VERSION | tr -d '\n'); printf '%s\n' \
		'{' \
		'  "name": "$(NPM_PKG_NAME)",' \
		"  \"version\": \"$$VERSION\"," \
		'  "description": "Generated TypeScript client for the DHBW Cloud $(PROJECT_NAME).",' \
		'  "license": "Apache-2.0",' \
		'  "type": "module",' \
		'  "exports": { ".": { "types": "./dist/entry.d.ts", "default": "./dist/index.mjs" } },' \
		'  "types": "./dist/entry.d.ts",' \
		'  "files": ["dist"],' \
		'  "publishConfig": { "access": "public" },' \
		'  "repository": { "type": "git", "url": "git+https://github.com/pfisterer/$(PROJECT_NAME).git" }' \
		'}' \
		> $(CLIENT_PKG_DIR)/package.json
	@# Bundled into one .mjs: the generated sources import each other without file extensions, which bundlers tolerate and plain Node does not.
	@npx esbuild $(CLIENT_PKG_DIR)/src/entry.ts --bundle --format=esm --outfile=$(CLIENT_PKG_DIR)/dist/index.mjs
	@npx tsc $(CLIENT_PKG_DIR)/src/entry.ts --declaration --emitDeclarationOnly \
		--outDir $(CLIENT_PKG_DIR)/dist --module esnext --moduleResolution bundler \
		--target es2022 --skipLibCheck
	@echo "✅ Package assembled in $(CLIENT_PKG_DIR)"

# The tarball a publish would upload — for trying the package in the UI without publishing.
npm-pack: npm-package
	@cd $(CLIENT_PKG_DIR) && npm pack

# A prerelease goes to the `next` dist-tag, so a plain `npm install` never hands out a -test.N build. Needs `npm login` once.
npm-publish: npm-package
	@npm whoami >/dev/null 2>&1 || npm login
	@VERSION=$$(cat VERSION | tr -d '\n'); \
	case "$$VERSION" in \
		*-*) TAG=next ;; \
		*)   TAG=latest ;; \
	esac; \
	echo "📤 Publishing $(NPM_PKG_NAME)@$$VERSION (dist-tag: $$TAG)"; \
	cd $(CLIENT_PKG_DIR) && npm publish --tag "$$TAG"

# ── Build ─────────────────────────────────────────────────────────────────────

build: check-modules
	@echo "🔨 Building Go binary..."
	@mkdir -p $(BUILD_DIR)
	@set -e; GOOS=$${BUILD_GOOS:-$$(go env GOOS)} GOARCH=$${BUILD_GOARCH:-$$(go env GOARCH)} go build -o $(BUILD_DIR)/$(BINARY_NAME) $(SRC_DIR)/main.go
	@echo "✅ Binary: ./$(BUILD_DIR)/$(BINARY_NAME)"

check-modules:
	@test -f $(GO_MOD) || (echo "❌ $(GO_MOD) missing; run 'go mod init' first."; exit 1)

# ── Dev / Run ─────────────────────────────────────────────────────────────────

dev:
	API_MODE=development air

run: build
	@echo "🚀 Running..."
	@./$(BUILD_DIR)/$(BINARY_NAME)

# ── Quality ───────────────────────────────────────────────────────────────────

test:
	@echo "🧪 Running tests..."
	@go test ./...
	@echo "✅ Tests passed"

tidy:
	go mod tidy

lint:
	golangci-lint run ./...

# ── Docker ────────────────────────────────────────────────────────────────────

docker-build: generate-docs
	@echo "🏗️ Building Docker image $(DOCKER_REPO):$(DOCKER_TAG)..."
	docker build --progress=plain -t "$(DOCKER_REPO):$(DOCKER_TAG)" .
	@echo "✅ $(DOCKER_REPO):$(DOCKER_TAG) built"

docker-run: docker-build
	docker run --rm -p 8085:8085 --env-file .env "$(DOCKER_REPO):$(DOCKER_TAG)"

docker-multi-arch-build: helm-update
	@echo "🏗️ Building multi-arch image for $(DOCKER_PLATFORMS)..."
	docker buildx build \
		--progress plain \
		--platform $(DOCKER_PLATFORMS) \
		--tag "$(DOCKER_REPO):latest" \
		--tag "$(DOCKER_REPO):$(DOCKER_TAG)" \
		--push \
		.
	@echo "✅ $(DOCKER_REPO):$(DOCKER_TAG) pushed"

# ── Maintenance ───────────────────────────────────────────────────────────────

# --- Versioning -------------------------------------------------------------
# VERSION is the single source of truth; Chart.yaml has to agree with it. That
# matters more than it used to: once the chart is published as an OCI artifact,
# the number in Chart.yaml IS the artifact version, so a mismatch ships one
# release's contents under another release's name. `version-check` runs in CI
# and FAILS on a mismatch rather than quietly rewriting the file — rewriting
# would hide exactly the mistake it is meant to catch.

# make bump V=1.2.3
bump:
	@test -n "$(V)" || { echo "usage: make bump V=<x.y.z>"; exit 1; }
	@printf '%s' "$(V)" > VERSION
	@$(MAKE) --no-print-directory helm-update

version-check:
	@v=$$(cat VERSION | tr -d '\n'); \
	cv=$$(awk '/^version:/{print $$2}' helm-chart/Chart.yaml); \
	av=$$(awk '/^appVersion:/{gsub(/"/,"",$$2); print $$2}' helm-chart/Chart.yaml); \
	if [ "$$v" != "$$cv" ] || [ "$$v" != "$$av" ]; then \
		echo "✗ VERSION is $$v, Chart.yaml says version=$$cv appVersion=$$av"; \
		echo "  fix with: make bump V=$$v"; \
		exit 1; \
	fi; \
	echo "✅ version $$v is consistent"

# Update helm chart version from VERSION file
# The defaults alone do not pass the schema on purpose (no API token is set), so the lint names an existing Secret the way every deployment does.
helm-update:
	helm lint helm-chart/ --set roleProviderService.existingSecret=lint
	@echo "✅ Helm chart linted successfully."
	@VERSION=$$(cat VERSION | tr -d '\n'); \
	sed -e "s/^version: .*/version: $$VERSION/" \
	    -e "s/^appVersion: .*/appVersion: \"$$VERSION\"/" \
	    helm-chart/Chart.yaml > helm-chart/Chart.yaml.tmp; \
	mv helm-chart/Chart.yaml.tmp helm-chart/Chart.yaml; \
	echo "✅ Updated helm-chart/Chart.yaml to version $$VERSION"

clean:
	@echo "🧹 Cleaning..."
	@rm -rf $(BUILD_DIR)
	@echo "✅ Done"

update-deps:
	@echo "📦 Updating Go dependencies..."
	go get -u ./...
	go mod tidy
	@echo "✅ Dependencies updated"

# ── Help ──────────────────────────────────────────────────────────────────────

help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "  all              → Generate docs and build binary (default)"
	@echo "  generate-docs    → Run swag to regenerate swagger.json + embedded.go"
	@echo "  doc              → Alias for generate-docs"
	@echo "  build            → Compile Go binary"
	@echo "  run              → Build and run"
	@echo "  dev              → Live-reload dev server (requires air)"
	@echo "  test             → Run Go tests"
	@echo "  tidy             → go mod tidy"
	@echo "  lint             → golangci-lint"
	@echo "  clean            → Remove build artifacts"
	@echo "  update-deps      → Update all Go dependencies"
	@echo "  docker-build     → Build Docker image"
	@echo "  docker-run       → Build and run Docker container"
	@echo "  docker-multi-arch-build → Build and push multi-arch image"
	@echo "  helm-update      → Lint and update Helm chart version from VERSION file"

# make publish — release from this machine instead of waiting for CI, for test and stable versions alike: runs the checks, builds the image for the cluster (linux/amd64; the build stage runs natively and cross-compiles, see the Dockerfile), pushes it and the Helm chart to GHCR, and only then pushes the commits. CI then finds the version published and just verifies that it builds; tags and GitHub releases of stable versions stay with CI. Refuses a dirty tree and a version that is already published, so what is in the registry is always exactly one commit. Needs `docker login ghcr.io` and `helm registry login ghcr.io` with a token that may write packages.
publish: version-check test
	@test -z "$$(git status --porcelain)" || { echo "✗ uncommitted changes — commit first"; exit 1; }
	@set -e; V=$$(tr -d '\n' < VERSION); I=ghcr.io/pfisterer/role-provider-service; \
	if docker buildx imagetools inspect "$$I:$$V" >/dev/null 2>&1; then echo "✗ $$I:$$V is already published — bump the version first"; exit 1; fi; \
	echo "🏗️  $$I:$$V (linux/amd64)"; \
	docker buildx build --platform linux/amd64 --tag "$$I:$$V" --tag "$$I:latest" --push .; \
	rm -rf tmp/chart && mkdir -p tmp/chart && helm package helm-chart/ --destination tmp/chart >/dev/null; \
	helm push tmp/chart/*.tgz oci://ghcr.io/pfisterer/charts; \
	git push; \
	echo "✅ $$I:$$V and its chart are published, commits pushed — CI only verifies"
