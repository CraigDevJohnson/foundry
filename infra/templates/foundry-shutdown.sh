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

for _ in $(seq 1 90); do
  pgrep -f /foundry/foundryvtt/main.js >/dev/null || break
  sleep 1
done
if pgrep -f /foundry/foundryvtt/main.js >/dev/null; then
  echo "Foundry is still running; leaving the host running" >&2
  pgrep -af /foundry/foundryvtt/main.js >&2
  exit 1
fi

if ! sync; then
  echo "sync failed; leaving the host running" >&2
  exit 1
fi

echo "Foundry is stopped"
