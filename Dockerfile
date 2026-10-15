## Stage 1: Builder image
# The build stage runs on the machine that builds (BUILDPLATFORM) and cross-compiles for the target: on an arm64 Mac that is native speed instead of emulation. No cgo is needed, so the binary is static.
# Base images are pinned by tag and digest: a floating tag would put whatever was published that day into a release. Bump both lines on purpose.
FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS builder
ARG TARGETOS TARGETARCH

RUN apk add --no-cache git make build-base

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY Makefile ./
COPY VERSION ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/

RUN CGO_ENABLED=0 BUILD_GOOS=$TARGETOS BUILD_GOARCH=$TARGETARCH make all

## Stage 2: Production image
FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS final

WORKDIR /app

COPY --from=builder /app/tmp/build/role-provider-service /app/

EXPOSE 8085

# Run as a non-root user (numeric UID so Kubernetes can enforce runAsNonRoot
# without resolving names). Nothing in this image is written at runtime; the
# binary only needs to be readable and executable.
USER 65532:65532

CMD ["./role-provider-service"]
