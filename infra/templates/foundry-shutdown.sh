#!/bin/bash
# Shuts Foundry down before the host stops (the foundry-shutdown SSM document
# in infra/ci.tf, run by `task host:stop`). It takes no parameters. Any failure
# exits non-zero, and host:stop then leaves the instance running.
set -u

script=/foundry/stop_foundry.sh
if [ ! -f "$script" ]; then
  echo "$script is missing; leaving Foundry and the host running" >&2
  exit 1
fi

echo "Running $script"
# The legacy script may exit non-zero when Foundry is already stopped. The
# service stop and the process check below decide the outcome.
if ! bash "$script"; then
  echo "$script failed; checking Foundry anyway" >&2
fi

if ! systemctl stop foundry.service; then
  echo "systemctl stop foundry.service failed; leaving the host running" >&2
  exit 1
fi

# pgrep exits 1 when nothing matches; any other non-zero status means it
# could not check, which must not pass for "stopped".
gone=false
for _ in $(seq 1 90); do
  pgrep -f /foundry/foundryvtt/main.js >/dev/null
  rc=$?
  if [ "$rc" -eq 1 ]; then
    gone=true
    break
  elif [ "$rc" -ne 0 ]; then
    echo "pgrep failed with status $rc; leaving the host running" >&2
    exit 1
  fi
  sleep 1
done
if [ "$gone" != true ]; then
  echo "Foundry is still running; leaving the host running" >&2
  pgrep -af /foundry/foundryvtt/main.js >&2
  exit 1
fi

if ! sync; then
  echo "sync failed; leaving the host running" >&2
  exit 1
fi

echo "Foundry is stopped"
