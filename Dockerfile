# syntax=docker/dockerfile:1

FROM golang:1.23-alpine AS builder
WORKDIR /src
# go.mod uses remote replace directive for auth-client, no local copy needed
# Build context is the service directory root
COPY go.mod go.sum ./

RUN GOTOOLCHAIN=auto go mod download
COPY . .

RUN GOTOOLCHAIN=auto CGO_ENABLED=0 go build -o /out/inventory ./cmd/api
RUN GOTOOLCHAIN=auto CGO_ENABLED=0 go build -o /out/inventory-seed ./cmd/seed

FROM alpine:3.20
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /out/inventory /app/service
COPY --from=builder /out/inventory-seed /app/seed
COPY internal/ent/migrate/migrations ./internal/ent/migrate/migrations
# TLS certificates directory (optional, can be mounted as volume)
RUN mkdir -p ./config/certs
USER app
EXPOSE 4000
ENV PORT=4000
ENTRYPOINT ["/app/service"]

