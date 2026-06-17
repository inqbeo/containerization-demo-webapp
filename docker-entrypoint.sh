#!/bin/sh
set -e

# docker-entrypoint.sh — turn environment variables into a config file.
#
# Didactic point: the Go program does NOT read the environment directly. It only
# reads a YAML config file. This script is what bridges "12-factor env vars" to
# "a config file on disk" — the classic docker-entrypoint.sh templating pattern.

CONFIG_FILE="${CONFIG_FILE:-/etc/todoapp/config.yaml}"

# If no full DATABASE_URL was given but individual Postgres parts are present
# (e.g. injected from a CloudNativePG / Kubernetes Secret), assemble the DSN.
# Tip: if your password contains URL-special characters, prefer passing a ready
# DATABASE_URL (CNPG secrets provide a "uri" key for exactly this).
if [ -z "${DATABASE_URL:-}" ] && [ -n "${DB_HOST:-}" ]; then
  DATABASE_URL="postgres://${DB_USER}:${DB_PASSWORD}@${DB_HOST}:${DB_PORT:-5432}/${DB_NAME}?sslmode=${DB_SSLMODE:-disable}"
fi

if [ -f "$CONFIG_FILE" ]; then
  # Precedence rule for the lab: a config file that already exists (e.g. a
  # read-only bind mount / Kubernetes ConfigMap) WINS over the environment.
  echo "entrypoint: using existing config at $CONFIG_FILE (mounted)"
else
  echo "entrypoint: generating $CONFIG_FILE from environment"
  mkdir -p "$(dirname "$CONFIG_FILE")"
  # listen_addr is fixed at :8080 ON PURPOSE — the container always listens on
  # 8080; remapping to another port is an OUTSIDE-the-container concern
  # (docker -p HOST:8080, or a Service port -> targetPort 8080). It is NOT
  # derived from an env var, because when a Kubernetes Service named "app"
  # exists, Kubernetes injects APP_PORT=tcp://<clusterIP>:8080 into every pod
  # (legacy service-link vars). That would produce listen_addr ":tcp://...:8080"
  # and break the app. (Need a different port anyway? Mount a config file —
  # an existing config wins over this generated one, see above.)
  cat > "$CONFIG_FILE" <<EOF
company_name: "${COMPANY_NAME:-ACME Corp}"
listen_addr: ":8080"
data_dir: "${DATA_DIR:-/data}"
log_level: "${LOG_LEVEL:-info}"
log_requests: $( [ "${LOG_REQUESTS:-}" = "true" ] && echo true || echo false )
db_driver: "${DB_DRIVER:-sqlite}"
database_url: "${DATABASE_URL:-}"
auth_user: "${AUTH_USERNAME:-admin}"
auth_password: "${AUTH_PASSWORD:-}"
theme: "${THEME:-coral}"
EOF
  # Note: if AUTH_PASSWORD is unset, auth_password stays empty and the app
  # generates a random password at startup and prints it to the log.
fi

# Hand over to the main process as PID 1 so signals (SIGTERM) reach it directly.
# Without exec, the shell would stay PID 1 and swallow the signals.
exec "$@"
