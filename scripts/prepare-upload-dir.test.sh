#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEST_DIR="$(mktemp -d)"
BIN_DIR="$TEST_DIR/bin"
CHOWN_LOG="$TEST_DIR/chown.log"

cleanup() {
  rm -rf "$TEST_DIR"
}
trap cleanup EXIT

mkdir -p "$BIN_DIR"

cat >"$BIN_DIR/find" <<'SCRIPT'
#!/bin/sh
if [ "${FAKE_FIND_FOREIGN:-}" = "1" ]; then
  printf '%s\n' "$1/foreign"
fi
exit 0
SCRIPT

cat >"$BIN_DIR/chown" <<'SCRIPT'
#!/bin/sh
printf '%s\n' "$*" >>"$CHOWN_LOG"
exit 0
SCRIPT

chmod +x "$BIN_DIR/find" "$BIN_DIR/chown"
export PATH="$BIN_DIR:$PATH"
export CHOWN_LOG

run_prepare() {
  rm -f "$CHOWN_LOG"
  sh "$ROOT_DIR/docker/prepare-upload-dir.sh"
}

export LOCAL_UPLOAD_DIR="$TEST_DIR/uploads"
export FAKE_FIND_FOREIGN=1
run_prepare
if [ ! -d "$LOCAL_UPLOAD_DIR" ]; then
  echo "prepare did not create the upload directory"
  exit 1
fi
if [ "$(cat "$CHOWN_LOG")" != "-R nextjs:nodejs $LOCAL_UPLOAD_DIR" ]; then
  echo "prepare did not chown a foreign-owned upload tree: $(cat "$CHOWN_LOG" 2>/dev/null || true)"
  exit 1
fi

export FAKE_FIND_FOREIGN=0
run_prepare
if [ -e "$CHOWN_LOG" ]; then
  echo "prepare rewrote an upload tree already owned by nextjs"
  exit 1
fi

unset LOCAL_UPLOAD_DIR
export FAKE_FIND_FOREIGN=1
(
  cd "$TEST_DIR"
  sh "$ROOT_DIR/docker/prepare-upload-dir.sh"
)
if [ ! -d "$TEST_DIR/data/uploads" ]; then
  echo "prepare did not resolve the default upload directory from the working directory"
  exit 1
fi
if [ "$(cat "$CHOWN_LOG")" != "-R nextjs:nodejs $TEST_DIR/data/uploads" ]; then
  echo "prepare chowned the wrong resolved directory: $(cat "$CHOWN_LOG" 2>/dev/null || true)"
  exit 1
fi

echo "upload directory ownership repair ok"
