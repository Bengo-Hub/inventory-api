# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./

RUN GOTOOLCHAIN=auto go mod download
COPY . .

# Build all binaries: api, migrate, and seed
RUN GOTOOLCHAIN=auto CGO_ENABLED=0 go build -o /out/inventory ./cmd/api
RUN GOTOOLCHAIN=auto CGO_ENABLED=0 go build -o /out/inventory-migrate ./cmd/migrate
RUN GOTOOLCHAIN=auto CGO_ENABLED=0 go build -o /out/inventory-seed ./cmd/seed

FROM alpine:3.20
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /out/inventory /usr/local/bin/inventory
COPY --from=builder /out/inventory-migrate /usr/local/bin/inventory-migrate
COPY --from=builder /out/inventory-seed /usr/local/bin/inventory-seed
COPY internal/ent/migrate/migrations ./internal/ent/migrate/migrations
# Entrypoint script: wait for DB, run migrations, seed, then start server
COPY scripts/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh
# TLS certificates directory (optional, can be mounted as volume)
RUN mkdir -p ./config/certs
USER app
EXPOSE 4000
ENV PORT=4000
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
