---
status: accepted
date: 2026-10-13
---

# The Lobby calls the controller through a Function URL

The Lobby (craigdevjohnson.com) has replaced the portfolio as the management
interface: its Foundry Tool page, `/foundry`, is where the host's status is
shown. So the controller is an ordinary HTTP server behind a Lambda Function
URL with `AWS_IAM` auth, called the way The Lobby calls every Tool backend:
with requests signed by the site's role
([the-lobby ADR 0006](https://github.com/CraigDevJohnson/the-lobby/blob/main/docs/adr/0006-signed-requests-to-tool-backends.md)).
There is one function per Lobby site environment, `foundry-dev-controller` and
`foundry-prod-controller`, and each grants only that environment's site role,
`site-<env>-server`. The site's permissions boundary lets that role invoke only
functions named `*-<env>-*`, which is why the environment is in the name.

The site checks the Member's `foundry` grant before it calls, and the
controller believes the site's request without a sign-in of its own, so among
Members that grant is the gate on who sees the status. This first slice only reads:
`GET /server` reports EC2's state, the Systems Manager agent's connectivity and
whether Foundry's join page answers ([status API](../status-api.md)). Mutating
actions such as start and stop come later as their own routes, and will log
the Member the site names in `X-Lobby-Member` once they have checked that the
site's role signed the request. Until then, start and stop stay
with the Host control workflow and `task host:start` and `task host:stop`.
[ADR 0006](0006-the-lobby-starts-and-stops-the-host.md) adds them.

## What this supersedes

- [ADR 0001](0001-portfolio-and-foundry-responsibilities.md)'s portfolio as the
  operator interface. The Lobby is the interface; this repository still owns
  the backend, operations and host setup.
- [ADR 0003](0003-serverless-management-backend.md)'s portfolio invoking the
  controller server-side through the Lambda API. The Go Lambda controller
  stands; the site calls it through its Function URL.
- The direct-invoke Status API v1, never deployed, and its rule that no
  Function URL is part of the interface. The [status API](../status-api.md)
  now documents the HTTP contract.

ADR 0003 also required the portfolio to lose its direct EC2 start and stop
permission for this host when the backend took over. The Lobby has no such
permission, so the controller has nothing to replace there. Any the portfolio
still holds goes with the portfolio at The Lobby's cutover (aws-design §6.1
and §13).

## Considered options

- Keep direct Lambda invocation, with the site calling the AWS SDK. Rejected:
  The Lobby calls every Tool backend by signed web request, and an HTTP server
  also runs on a developer's computer (`task run`).
- One function for both site environments. Rejected: each site environment's
  role must be the only principal on its own function, so the dev site can
  never read through prod's grant. Separate functions also keep the
  `<tool>-<env>-*` name that The Lobby's function ARN validation expects.

## Consequences

- Each function's resource policy grants only its environment's site role.
  A principal in the same account whose own policy allows
  `lambda:InvokeFunctionUrl`, such as the admin profile, can call it too, as
  the deploy check in [infra/README.md](../../infra/README.md) does. An
  unsigned request gets `403` from AWS and never reaches the controller.
- The controller never sees the Member's grants. Among Members, the site's
  `foundry` grant is the gate: anyone the site lets onto `/foundry` sees the
  status.
- A mutating route must not take `X-Lobby-Member` on any signed caller's
  word. Before it trusts the header, it checks that the caller the Function
  URL verified (the request context's `authorizer.iam.userArn`) is the site
  role, as the Minecraft launcher does.
- The functions follow the host: `FOUNDRY_INSTANCE_ID` comes from
  `aws_instance.host`, so the apply that rebuilds the host also updates them.
  In between, `GET /server` answers `no_server`.
- A function runs only when the page asks, so it adds no measurable cost.
