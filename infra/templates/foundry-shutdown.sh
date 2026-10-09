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

# Prints the PIDs of processes that could still be writing Foundry data: any
# working directory under /foundry, or a command line naming foundryvtt,
# /foundry or --dataPath. That covers relative launches such as `node main.js`
# from /foundry/foundryvtt, which systemd and a path match would both miss.
# Returns 2 if a live process can't be inspected.
foundry_writers() {
  local dir pid cwd cmdline
  for dir in /proc/[0-9]*; do
    pid=${dir#/proc/}
    [ "$pid" = "$$" ] || [ "$pid" = "$BASHPID" ] && continue
    if ! cmdline=$(tr '\0' ' ' 2>/dev/null <"$dir/cmdline"); then
      [ -d "$dir" ] || continue
      echo "Cannot inspect process $pid" >&2
      return 2
    fi
    # Kernel threads and zombies have an empty command line and can't write.
    [ -n "$cmdline" ] || continue
    if ! cwd=$(readlink "$dir/cwd" 2>/dev/null); then
      [ -d "$dir" ] || continue
      echo "Cannot inspect process $pid" >&2
      return 2
    fi
    case "$cwd" in
      /foundry | /foundry/*) echo "$pid $cmdline (in $cwd)"; continue ;;
    esac
    case "$cmdline" in
      *[Ff]oundry[Vv][Tt][Tt]* | */foundry/* | *--dataPath*) echo "$pid $cmdline" ;;
    esac
  done
}

gone=false
for _ in $(seq 1 90); do
  if ! writers=$(foundry_writers); then
    echo "Could not check for Foundry processes; leaving the host running" >&2
    exit 1
  fi
  if ! listeners=$(ss -H -ltn 'sport = :30000'); then
    echo "Could not check port 30000; leaving the host running" >&2
    exit 1
  fi
  if [ -z "$writers" ] && [ -z "$listeners" ]; then
    gone=true
    break
  fi
  sleep 1
done
if [ "$gone" != true ]; then
  echo "Foundry is still running; leaving the host running" >&2
  [ -z "$writers" ] || echo "$writers" >&2
  [ -z "$listeners" ] || echo "Listening on port 30000: $listeners" >&2
  exit 1
fi

if ! sync; then
  echo "sync failed; leaving the host running" >&2
  exit 1
fi

echo "Foundry is stopped"
