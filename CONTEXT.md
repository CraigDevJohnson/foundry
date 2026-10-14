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
Craig is the sole operator for the first release.
_Avoid_: Player (when referring to management authority)

**Player**:
A person using the hosted Foundry VTT application for tabletop play. Participation
in a game does not itself grant management authority.

**Foundry VTT service**:
The running Foundry VTT application on the host. Restarting this service is a
separate operation from stopping and starting the host.
_Avoid_: Host restart, EC2 restart (when referring to an application restart)

**Session**:
A period of tabletop play or solo GM preparation on the hosted Foundry VTT
installation.

**Start session**:
The operator-requested action that starts the host for play or preparation.
_Avoid_: Launch world (which concerns activity inside Foundry VTT)

**End session**:
The operator-requested action that backs up the installation before stopping
the host after play or preparation. A failed backup prevents normal shutdown.
_Avoid_: Stop (when the backup step is part of the requested action)

**Session backup**:
A recovery copy of the hosted installation's required data and configuration
captured during End session, preserving the completed play or preparation work.
_Avoid_: World backup (when the copy covers the installation rather than one world)

**Scheduled recovery point**:
A periodic recovery copy triggered independently of End session, subject to
application-consistent capture. It does not count as a session backup even if it
contains completed session state.

**Pre-upgrade backup**:
A recovery copy captured before an upgrade and retained until the operator
explicitly releases it.

**Restore drill**:
An isolated restoration of a recovery copy that demonstrates the hosted game
can be recovered. It is distinct from checking a backup's integrity.
_Avoid_: Integrity check (when referring to demonstrated recovery)
