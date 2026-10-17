#!/bin/sh
set -eu

# All-in-one supervisor: migrate, then run the API server and the Next.js
# standalone server as sibling processes. Either one dying stops the other so
# the container reflects the failure instead of serving a half-dead app.

# The migrator and API server are separate processes, but startup retry time is
# one container-level budget. Both binaries derive their remaining time from
# this entrypoint timestamp. Always overwrite inherited values to avoid reusing
# a stale timestamp after a container restart.
export MULTICA_INTERNAL_DATABASE_STARTUP_STARTED_AT_UNIX="$(date +%s)"

BACKEND_PORT="${BACKEND_PORT:-8080}"
FRONTEND_PORT="${FRONTEND_PORT:-3000}"

migrate_pid=""
server_pid=""
web_pid=""

stop_all() {
  signal="$1"
  exit_code="$2"
  trap - TERM INT
  for pid in "$migrate_pid" "$server_pid" "$web_pid"; do
    if [ -n "$pid" ]; then
      kill "-$signal" "$pid" 2>/dev/null || true
    fi
  done
  for pid in "$migrate_pid" "$server_pid" "$web_pid"; do
    if [ -n "$pid" ]; then
      wait "$pid" 2>/dev/null || true
    fi
  done
  exit "$exit_code"
}

trap 'stop_all TERM 143' TERM
trap 'stop_all INT 130' INT

echo "Running database migrations..."
./migrate up &
migrate_pid=$!
if wait "$migrate_pid"; then
  migrate_status=0
else
  migrate_status=$?
fi
migrate_pid=""
if [ "$migrate_status" -ne 0 ]; then
  exit "$migrate_status"
fi

echo "Starting API server on port $BACKEND_PORT..."
PORT="$BACKEND_PORT" ./server &
server_pid=$!

echo "Starting web frontend on port $FRONTEND_PORT..."
PORT="$FRONTEND_PORT" HOSTNAME=0.0.0.0 node apps/web/server.js &
web_pid=$!

# BusyBox ash has no `wait -n`; poll until one child exits, then stop the other
# and propagate the dead child's exit code.
while kill -0 "$server_pid" 2>/dev/null && kill -0 "$web_pid" 2>/dev/null; do
  sleep 1
done

if ! kill -0 "$server_pid" 2>/dev/null; then
  status=0
  wait "$server_pid" || status=$?
  echo "API server exited with status $status; stopping web frontend..."
  kill "$web_pid" 2>/dev/null || true
  wait "$web_pid" 2>/dev/null || true
  exit "$status"
fi

status=0
wait "$web_pid" || status=$?
echo "Web frontend exited with status $status; stopping API server..."
kill "$server_pid" 2>/dev/null || true
wait "$server_pid" 2>/dev/null || true
exit "$status"
