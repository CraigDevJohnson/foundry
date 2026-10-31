# Foundry management

The operation of Craig's hosted Foundry VTT installation and its host.

## Language

**Management plane**:
The tools through which an operator manages the hosted Foundry VTT installation
and its host.
_Avoid_: Foundry VTT (when referring to management tools)

**Controller**:
The management plane's backend: the Lambda function The Lobby's site calls for
the host's status, and to start the host or send it the stop. One runs for
each Lobby site environment.

**Deploy run**:
A run of the Deploy workflow, which Craig starts and approves. A plan run
posts the plan and its digest; an apply run applies only a plan with the
digest Craig gives it.
_Avoid_: Release, rollout

**Foundry VTT**:
Foundry Virtual Tabletop, the application Craig hosts for tabletop play.
_Avoid_: Management plane

**Host**:
The compute environment on which the hosted Foundry VTT application runs.
_Avoid_: Foundry VTT (when referring to the machine)

**Operator**:
The person responsible for managing the hosted installation and its host.
Craig is the operator. Any Member with The Lobby's `foundry` or `foundry-admin`
grant may also start and stop the host (ADR 0006).
_Avoid_: Player (when referring to management authority)

**Player**:
A person using the hosted Foundry VTT application for tabletop play. Participation
in a game does not itself grant management authority; a Member's Foundry grant
in The Lobby does, for start and stop only.

**Foundry VTT service**:
The running Foundry VTT application on the host. Restarting this service is a
separate operation from stopping and starting the host.
_Avoid_: Host restart, EC2 restart (when referring to an application restart)

**Reverse proxy**:
nginx on the host, which answers `https://foundry.craigdevjohnson.com` on
port 443 and hands each request to the Foundry VTT service on port 30000,
which only the host can reach (ADR 0008).
_Avoid_: Load balancer, Cloudflare proxy (neither is in use)

**Session**:
A period of tabletop play or solo GM preparation on the hosted Foundry VTT
installation.

**Start session**:
The action that starts the host for play or preparation.
_Avoid_: Launch world (which concerns activity inside Foundry VTT)

**End session**:
The action that shuts Foundry down cleanly and then stops the host after play
or preparation. If the shutdown fails, the host keeps running. It takes no
backup of its own: the next scheduled recovery point holds the finished
session.
_Avoid_: Power off (when Foundry has not been shut down first)

**Session backup**:
A recovery copy captured during End session. None is taken: Craig dropped the
step on 2026-10-16 (issue #7), and older documents that name it describe the
earlier plan.
_Avoid_: World backup (when the copy covers the installation rather than one world)

**Scheduled recovery point**:
A Data Lifecycle Manager snapshot of both volumes, taken daily and weekly
whether or not Foundry is running, so it holds the data as of the snapshot.
The first one after a session is that session's backup.

**Pre-upgrade backup**:
A recovery copy captured before an upgrade and retained until the operator
explicitly releases it.

**Restore drill**:
An isolated restoration of a recovery copy that demonstrates the hosted game
can be recovered. It is distinct from checking a backup's integrity.
_Avoid_: Integrity check (when referring to demonstrated recovery)
