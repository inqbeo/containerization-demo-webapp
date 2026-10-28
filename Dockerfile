# syntax=docker/dockerfile:1

# --- Stage 1: build ---------------------------------------------------------
# Build a fully static binary (CGO_ENABLED=0). Because the SQLite and Postgres
# drivers are pure Go, there is nothing to link against — the result runs on a
# bare alpine without any system libraries.
FROM golang:1.25-alpine AS build
WORKDIR /src

# Download dependencies first so this layer is cached across code changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /out/todo .

# --- Stage 2: runtime -------------------------------------------------------
# Deliberately alpine (not distroless) because docker-entrypoint.sh needs a
# shell. Distroless would be smaller/cleaner for production but has no shell —
# that trade-off is itself a teaching point (see README / spec §9).
FROM alpine:3.20
RUN adduser -D -u 10001 app \
 && mkdir -p /data /etc/todoapp \
 && chown -R app /data /etc/todoapp

COPY --from=build /out/todo /usr/local/bin/todo
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

USER app
VOLUME ["/data"]
# 8080 = the app, 8081 = GET /metrics (ops port, keep it cluster-internal).
EXPOSE 8080 8081

# Container-level health check. In Kubernetes this splits into two probes:
# liveness → /livez (process alive), readiness → /readyz (= /healthz, DB reachable).
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s \
  CMD wget -qO- http://localhost:8080/healthz >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
CMD ["todo"]
