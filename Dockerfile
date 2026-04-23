## Stage 1: Builder image
FROM golang:1-alpine AS builder

RUN apk add --no-cache git make build-base

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY Makefile ./
COPY VERSION ./
COPY cmd/ ./cmd/
COPY internal/ ./internal/

RUN make all

## Stage 2: Production image
FROM alpine:latest AS final

WORKDIR /app

COPY --from=builder /app/tmp/build/role-provider-service /app/

EXPOSE 8085

CMD ["./role-provider-service"]
