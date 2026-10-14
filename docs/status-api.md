# Status API

The controller's HTTP interface: what The Lobby's site asks for its Foundry
Tool, and what each answer means. It reports the host's status, starts the
host and stops it ([ADR 0006](adr/0006-the-lobby-starts-and-stops-the-host.md)).
`task host:start`, `task host:stop` and the Host control workflow
([operations](operations.md)) still work beside it.

This replaces the direct-invoke "Status API v1" (a JSON
`{"version":1,"action":"get_status",...}` payload sent with Lambda Invoke),
which was never deployed and is retired. [ADR 0005](adr/0005-the-lobby-calls-the-controller-through-a-function-url.md)
records why.

## Who calls it

The controller runs as one Lambda function per Lobby site environment,
`foundry-dev-controller` and `foundry-prod-controller`
([infra/controller.tf](../infra/controller.tf)). Each serves this interface at
the root of its own Function URL, which uses `AWS_IAM` auth and grants only
that environment's site role, `site-<env>-server`. The site signs each request
([the-lobby ADR 0006](https://github.com/CraigDevJohnson/the-lobby/blob/main/docs/adr/0006-signed-requests-to-tool-backends.md));
an unsigned request gets `403` from AWS and never reaches the controller.

The controller does no sign-in of its own. The site checks the Member's grant
before it calls: `foundry` or `foundry-admin`, for the status and for start
and stop alike.
Those grants are the gate on which Members see the status and change the
host. The site names the Member in the `X-Lobby-Member` header. Reads ignore it
and never log it. Start and stop log it as who asked, but only when the
request was signed by this environment's site role (see [Who asked](#who-asked)).

The controller ignores query strings, every other header and every request
body but the stop's confirmation. No request can choose an instance, Region,
URL, AWS role or command: those come only from the function's configuration.

## `GET /server`

`200` with the host's status:

```json
{
  "state": "ready",
  "join_url": "https://foundry.craigdevjohnson.com:30000/join",
  "started_at": "2026-10-13T18:00:00Z",
  "checked_at": "2026-10-13T18:03:12Z",
  "checks": {
    "host": "running",
    "agent": "online",
    "foundry": "responding",
    "stop": "none"
  }
}
```

- `state` is one of:

  | State | When |
  | --- | --- |
  | `stopped` | EC2 reports the host `stopped`. |
  | `starting` | EC2 reports `pending`. Or EC2 reports `running`, the join page has not answered 200 yet, and EC2 started the host less than 10 minutes ago. |
  | `ready` | EC2 reports `running`, no stop is under way, and the join page answered 200. Nothing else is ever `ready`. |
  | `stopping` | EC2 reports `stopping` or `shutting-down`. Or EC2 reports `running` and a stop is under way: `checks.stop` is `in_progress`, or `succeeded` less than 15 minutes after the stop was sent, while the host powers off. |
  | `unresponsive` | EC2 reports `running`, the join page does not answer 200, and EC2 started the host 10 minutes ago or more, or gave no start time. The host is on but Foundry is not answering players: no world is open, Foundry shows its license page after a rebuild, it crashed, or a stop shut it down but the host never powered off. |
  | `unknown` | EC2's state could not be read: a timeout, access denied, a provider error or a record that does not match. Or EC2 reports `running` and the stop recorded on the host could not be read (`checks.stop` is `unknown`), so a stop may be under way. It is never guessed. |

  The 10 minutes count from EC2's `LaunchTime`, which EC2 resets on every
  start. The agent's state never changes `state`. A stop under way comes
  first: a running host is `stopping` then, whatever the join page answers.
- `join_url`: the join page players open, the configured `FOUNDRY_HTTPS_URL`.
  Always present.
- `started_at`: when EC2 last started the host (its `LaunchTime`), only while
  EC2 reports `pending` or `running` and gives a time. Left out otherwise.
- `checked_at`: when the observation finished.
- `checks`: the three raw observations, for the operator and the page's detail
  line:

  | Check | Values | Meaning |
  | --- | --- | --- |
  | `host` | `pending`, `running`, `stopping`, `stopped`, `shutting-down`, `unknown` | EC2's lifecycle state for the configured instance |
  | `agent` | `online`, `offline`, `unknown`, `not_checked` | Whether the Systems Manager agent is connected. A missing record is `unknown`, not `offline` |
  | `foundry` | `responding`, `unexpected_response`, `unreachable`, `unknown`, `not_checked` | Whether the join page answered HTTP 200 within the time and size limits |
  | `stop` | `none`, `in_progress`, `succeeded`, `failed`, `unknown`, `not_checked` | The stop recorded on the host, if it was sent since EC2 last started the host: none, still running (or being cancelled), done (Foundry is down and the host powers off a minute later), or failed, timed out or cancelled (the host stays on). A stop whose record Systems Manager no longer keeps is `none`: it can't still be running |

  `agent`, `foundry` and `stop` are `not_checked` unless EC2 reports the host
  `running`; `stop` is also `not_checked` when EC2 gave no start time to count
  from.

Times are UTC in RFC 3339 with whole seconds. Reasons, raw provider errors,
page content, instance IDs and IP addresses are never in an answer.

`404` with `{"code": "no_server"}` when EC2 reports the configured instance
`terminated` or does not find it, for example in the middle of a rebuild.

## `POST /server/start`

Starts a stopped host. The body is ignored.

- `202` with the status, in the shape of `GET /server`, when EC2 accepted the
  start: `state` `starting`, `checks.host` as EC2 reported it after the start
  (normally `pending`), the other checks `not_checked`, and no `started_at`
  yet. Foundry starts with the host.
- `200` with the current status when there is nothing to start: the host is
  `pending` or `running` with no stop under way, whatever its `state`. A start
  never restarts Foundry on a running host.
- `409` with `{"code": "stopping"}` while the host is stopping or a stop is
  under way, including when EC2 refuses the start because the host is not
  stopped yet. Start it once it is `stopped`.
- `404` with `{"code": "no_server"}` as for `GET /server`.
- `503` with `{"code": "status_unknown"}` when EC2's state could not be read,
  or `{"code": "start_failed"}` when EC2 refused the start for another reason
  or its answer didn't arrive in time. The start may have happened then, and
  the status shows it if so. Nothing is retried.

## `POST /server/stop`

Shuts Foundry down and powers the host off. The controller can't see who is
playing, so the body must be the Member's confirmation that nobody is:

```json
{"nobody_playing": true}
```

A JSON object of at most 1 KiB whose `nobody_playing` is `true`; other fields
are ignored. Anything else answers `400` with
`{"code": "confirmation_required"}` before the host is observed.

- `202` with the status, `state` `stopping` and `checks.stop` `in_progress`,
  when the `foundry-stop` command was sent. It shuts Foundry down as
  `task host:stop` does, then powers the host off a minute later; EC2 stops
  it. Right after sending it, the controller records it on the host's tags,
  `foundry:stop-command` and `foundry:stop-requested`, and the status follows
  it from there: `stopping`, then `stopped`. If the shutdown fails, the host
  stays on, `checks.stop` turns `failed` and the state follows the host
  again. If the tags can't be written, the answer is still `202` and the log
  says `sent_not_recorded`: the status can't show that stop until the host
  powers off.
- `200` with the current status when there is nothing to stop: the host is
  already `stopped` or `stopping`, or a stop is under way. So a stop requested
  after another was sent answers this way and sends nothing. Two requests at
  the same moment, such as one from each site environment, can each send
  one. On the host only the first runs the shutdown; the second waits for it,
  or finds its success if it arrives just after, and ends the same way, so
  the status shows the stop whichever of the two it records.
- `409` with `{"code": "starting"}` while EC2 reports the host `pending`.
  Stop it once it is running.
- `409` with `{"code": "agent_offline"}` when the host has no connected
  Systems Manager agent to send the stop to, for example in the first minute
  after it booted. Nothing was sent.
- `404` with `{"code": "no_server"}` as for `GET /server`.
- `503` with `{"code": "status_unknown"}` when EC2's state, or the stop
  recorded on the host, could not be read: a second stop could overlap the
  first, so none is sent. `{"code": "stop_failed"}` when Systems Manager
  refused the command for another reason or its answer didn't arrive in time.
  The command may have been sent then, but without its ID the controller
  can't record it, so `checks.stop` doesn't show it and the status follows
  the host until it powers off. The controller retries nothing; a second
  stop the Member asks for waits on the host for the first and ends the same
  way.

A stop takes no backup; it is not End session. The command has a 60-second
delivery timeout and gives the shutdown 5 minutes to run. AWS's documentation
differs on when an undelivered command expires: SendCommand's reference says
one that hasn't started within the delivery timeout won't run, and Systems
Manager's user guide adds the 5 minutes to that. So a stop the agent misses
ends, at the latest, about 6 minutes after it was sent, as `failed`, and
`checks.stop` is `in_progress`, so the host shows `stopping`, until then.

## Who asked

Each start and stop writes a `start` or `stop` log line with what happened
(`outcome`) and who asked. `caller` is the identity AWS verified for the
request's signature, from the Function URL's request context: for the site,
`arn:aws:sts::<account>:assumed-role/site-<env>-server/<session>`; for an
operator calling with the admin profile, their own role session. When the
caller is the role in `FOUNDRY_SITE_ROLE_ARN`, the line also carries `member`,
the Member the site named in `X-Lobby-Member`, or `unreadable` when that is not
a plain email address. Any other caller's `X-Lobby-Member` is ignored. On a
developer's computer there is no request context, and `caller` is `local`. A
stop refused for its missing confirmation is logged too, with the outcome
`confirmation_required`.

## Other routes

- `GET /healthz` answers `204` with no body and observes nothing. The
  post-deploy check in [infra/README.md](../infra/README.md) calls it signed.
- An unknown path answers `404` with `{"code": "not_found"}`.
- A known path asked with another method answers `405` with
  `{"code": "method_not_allowed"}` and an `Allow` header: `GET, HEAD` for
  `/server` and `/healthz`, `POST` for `/server/start` and `/server/stop`.
  `HEAD` is answered like `GET`, without the body.

Every answer carries `Cache-Control: no-store`, and every JSON body
`Content-Type: application/json`.

## How it observes

1. EC2 `DescribeInstances` for the configured instance only, which also gives
   its tags.
2. If EC2 reports it `running`, at the same time and independently of each
   other: Systems Manager `DescribeInstanceInformation` for that instance, an
   HTTPS `GET` of the join page, and, when the host's tags record a stop sent
   since EC2's `LaunchTime`, Systems Manager `GetCommandInvocation` for that
   command on that instance, which decides `checks.stop`. Systems Manager may
   have no record of a command for a short while after it is sent: for 2
   minutes that counts as `in_progress`, and after that as `unknown`. From 10
   minutes after it was sent, when no stop can still be running, a missing
   record means Systems Manager's history has expired, and it counts as
   `none`. A tag
   that is not a command ID or a time, or a record for another command,
   instance or document, is `unknown`. The controller never lists commands:
   `ListCommands` would show every command sent in the account, with its
   parameters.

They are taken during one request, not as an atomic snapshot. The whole
observation is bounded at 6 seconds, because the site's client gives up at 7
and its router at 8; each provider call and the probe keep their own 3-second
limit. A start or a stop gets 6 seconds in all: at most 4 for the observation,
and the rest for the one EC2 or Systems Manager call. A stop that was sent is
then recorded on the host's tags with 2 seconds of its own, even when those 6
are spent, because only a recorded stop can be followed. The Lambda timeout is
10 seconds. A call cut off by the bound reports `unknown` (or `unreachable` for
the probe), never a guess.

The probe verifies TLS, follows no redirects and reads at most 64 KiB of the
body, which it discards. Only HTTP 200 within those limits is `responding`; a
redirect (such as to the license page), another status or a larger body is
`unexpected_response`.

## Logs

The controller writes JSON lines to standard error, which Lambda sends to the
function's log group. Each `GET /server` writes a `status` line with the
reported state, each check and its reason, the join page's HTTP status and the
agent's last ping. Each start and stop writes a `start` or `stop` line ([Who
asked](#who-asked)) with its outcome (`started`, `sent`, `unchanged`,
`refused_stopping`, `refused_starting`, `refused_agent_offline`, `no_server`,
`status_unknown` or `failed`, with the error), the host and stop checks with
their reasons, EC2's state after a start and the stop's command ID. Reasons are fixed categories: `observed`, `no_record`,
`unexpected_record`, `unexpected_state`, `access_denied`, `timeout`,
`canceled`, `provider_error`, `unexpected_status`, `response_too_large`,
`tls_error`, `network_error`, `response_error`, `host_not_running`,
`host_unknown` and `no_launch_time`. The line is a warning when EC2 could not
be read. Every request also writes a `request` line with the method, the route
pattern (never the path), the status code and the time taken. No line carries
a header or a body, and only a start or stop line carries the Member.

## Configuration

The first three settings are required:

| Variable | Deployed value |
| --- | --- |
| `FOUNDRY_AWS_REGION` | `us-west-2` |
| `FOUNDRY_INSTANCE_ID` | The host's instance ID, from `aws_instance.host`, so a rebuild updates it |
| `FOUNDRY_HTTPS_URL` | `https://foundry.craigdevjohnson.com:30000/join` |
| `FOUNDRY_SITE_ROLE_ARN` | `arn:aws:iam::<account>:role/site-<env>-server`, the environment's site role. Optional: unset, no caller's Member is logged |

The URL must be exactly `https://host[:port]/join`, with no credentials, query
or fragment, and the role an IAM role ARN. A missing or invalid setting stops
the controller before it serves: it logs `invalid_configuration` with the
setting's name and exits with status 1. On Lambda that is an init error, which
the function's `foundry-<env>-controller-failing` alarm counts. AWS credentials
come from the SDK's standard chain: the function's role on Lambda, your own
locally.

The runtime needs `ec2:DescribeInstances`, `ssm:DescribeInstanceInformation`
and `ssm:GetCommandInvocation` to observe, `ec2:StartInstances` on the host to
start it, `ssm:SendCommand` on the host and the `foundry-stop` document and
`ec2:CreateTags` on the host for its two stop tags to stop it, and its own log
group. It can't call `ec2:StopInstances` or send any other
command.

## Running it locally

`task run` serves the same interface at `http://127.0.0.1:8083` with the
credentials in your environment. Reading the status changes nothing:

```sh
AWS_PROFILE=workloads-readonly task run
curl -s http://127.0.0.1:8083/server
```

The three required settings default to `us-west-2`, the host found by its
`Name=foundry` and `project=foundry` tags, and the join page; set any of them
to override it. `POST /server/start` and `POST /server/stop` change the real
host with your credentials, so they need an admin profile and Craig's approval
like any other change; `workloads-readonly` is refused. `FOUNDRY_ADDR` changes the address,
which must be a loopback address such as `127.0.0.1:9000`: the server has no
sign-in, so it refuses to start on an address another computer could reach.
It answers only requests addressed to `localhost` or a loopback address
(`421` with `{"code": "not_local"}` otherwise) and refuses a browser's
cross-origin `POST` with `403`, so a web page open on your computer can't use
your credentials through it; curl is unaffected. The Lobby's site can't
call it from a developer's computer: sign-in is disabled there, so the site's
`/api/foundry/server` answers `401` before it calls any backend. Ask the
controller directly, as above.

## Build and checks

`task ci` checks formatting, vets, runs the tests with the race detector,
builds the Lambda artifact and scans it with govulncheck. The tests use fake
providers and local test servers, never AWS. `task build` writes
`dist/bootstrap` and `dist/bootstrap.zip`: one static Linux ARM64 executable
named `bootstrap` at the zip's root, for the `provided.al2023` runtime. The
same source gives the same zip, so a plan with no code change reports no
change to the function. Build output is ignored by Git.
