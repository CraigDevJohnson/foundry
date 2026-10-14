# Appended to foundry-shutdown.sh in the foundry-stop document
# (infra/controller.tf), which the controller sends when The Lobby stops the
# host (ADR 0006). It runs only after that script has stopped Foundry, found no
# process left writing Foundry data and synced /foundry: any failure there has
# already exited non-zero and left the host running. The power-off waits a
# minute so Systems Manager records the command's success before the agent
# goes down. The host's instance-initiated shutdown behavior is stop, so EC2
# stops the instance; its volumes and Elastic IP stay.
if ! shutdown --poweroff +1 "Foundry is stopped; the host powers off for The Lobby"; then
  echo "Could not schedule the power-off; Foundry is stopped but the host is still running" >&2
  exit 1
fi
echo "The host powers off in a minute"
