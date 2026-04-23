PROJECT_NAME := role-provider-service
BINARY_NAME  := $(PROJECT_NAME)
SRC_DIR      := ./cmd
DOC_DIR      := ./internal/generated_docs
BUILD_DIR    := ./tmp/build
GO_MOD       := go.mod

SWAGGER_JSON := $(DOC_DIR)/swagger.json
EMBED_FILE   := $(DOC_DIR)/embedded.go

# Docker image details
DOCKER_REPO      ?= ghcr.io/pfisterer/$(PROJECT_NAME)
DOCKER_TAG       ?= $(shell cat VERSION 2>/dev/null || echo "dev")
DOCKER_PLATFORMS ?= linux/amd64,linux/arm64

.DEFAULT_GOAL := all

.PHONY: all build clean doc check-swag generate-docs run dev test tidy lint help \
        docker-build docker-run docker-multi-arch-build

all: generate-docs build

# ── Documentation ─────────────────────────────────────────────────────────────

# Ensure swag is installed
check-swag:
	@command -v swag >/dev/null 2>&1 || go install github.com/swaggo/swag/cmd/swag@latest

# Generate swagger.json from inline annotations and write embedded.go
generate-docs: check-swag
	@echo "📚 Generating swagger.json..."
	@mkdir -p $(DOC_DIR)
	@set -e; swag init -g doc.go --dir $(SRC_DIR),./internal/webserver,./internal/common -o $(DOC_DIR) --outputTypes json
	@echo "🧩 Writing $(EMBED_FILE)..."
	@printf '%s\n' \
		'package generated_docs' \
		'' \
		'import _ "embed"' \
		'' \
		'//go:embed swagger.json' \
		'var SwaggerJSON string' \
		> $(EMBED_FILE)
	@echo "✅ Docs generated in $(DOC_DIR)/"

# Alias
doc: generate-docs

# ── Build ─────────────────────────────────────────────────────────────────────

build: check-modules
	@echo "🔨 Building Go binary..."
	@mkdir -p $(BUILD_DIR)
	@set -e; go build -o $(BUILD_DIR)/$(BINARY_NAME) $(SRC_DIR)/main.go
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

docker-multi-arch-build:
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
