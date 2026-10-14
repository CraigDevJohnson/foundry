# Run by the foundry-stop document (templates/foundry-stop.sh.tftpl), which the
# controller sends when The Lobby stops the host (ADR 0006), only after
# foundry-shutdown.sh has exited 0: it stopped Foundry, found no process left
# writing Foundry data and synced /foundry. Any failure there leaves the host
# running. The power-off waits a minute so Systems Manager records the
# command's success before the agent goes down. The host's instance-initiated
# shutdown behavior is stop, so EC2 stops the instance; its volumes and Elastic
# IP stay.
if ! shutdown --poweroff +1 "Foundry is stopped; the host powers off for The Lobby"; then
  echo "Could not schedule the power-off; Foundry is stopped but the host is still running" >&2
  exit 1
fi
echo "The host powers off in a minute"
