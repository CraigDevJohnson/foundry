# Status API

The controller's HTTP interface: what The Lobby's site asks for its `/foundry`
page, and what each answer means. It reports the host's status and changes
nothing. Starting and stopping are not part of it yet; they stay with
`task host:start`, `task host:stop` and the Host control workflow
([operations](operations.md)).

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

The controller does no sign-in of its own. The site checks the Member's
`foundry` grant before it calls, so that grant is the gate on which Members
see the status. The site names the Member in the `X-Lobby-Member` header. Reads
ignore it and never log it; start and stop, when they come, will log it as who
asked once they have checked that the site's role signed the request.

The controller ignores request bodies, query strings and every other header.
No request can choose an instance, Region, URL, AWS role or command: those come
only from the function's configuration.

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
    "foundry": "responding"
  }
}
```

- `state` is one of:

  | State | When |
  | --- | --- |
  | `stopped` | EC2 reports the host `stopped`. |
  | `starting` | EC2 reports `pending`. Or EC2 reports `running`, the join page has not answered 200 yet, and EC2 started the host less than 10 minutes ago. |
  | `ready` | EC2 reports `running` and the join page answered 200. Nothing else is ever `ready`. |
  | `stopping` | EC2 reports `stopping` or `shutting-down`. |
  | `unresponsive` | EC2 reports `running`, the join page does not answer 200, and EC2 started the host 10 minutes ago or more, or gave no start time. The host is on but Foundry is not answering players: no world is open, Foundry shows its license page after a rebuild, or it crashed. |
  | `unknown` | EC2's state could not be read: a timeout, access denied, a provider error or a record that does not match. It is never guessed. |

  The 10 minutes count from EC2's `LaunchTime`, which EC2 resets on every
  start. The agent's state never changes `state`.
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

  `agent` and `foundry` are `not_checked` unless EC2 reports the host
  `running`.

Times are UTC in RFC 3339 with whole seconds. Reasons, raw provider errors,
page content, instance IDs and IP addresses are never in an answer.

`404` with `{"code": "no_server"}` when EC2 reports the configured instance
`terminated` or does not find it, for example in the middle of a rebuild.

## Other routes

- `GET /healthz` answers `204` with no body and observes nothing. The
  post-deploy check in [infra/README.md](../infra/README.md) calls it signed.
- An unknown path answers `404` with `{"code": "not_found"}`.
- A known path asked with another method answers `405` with
  `{"code": "method_not_allowed"}` and an `Allow: GET, HEAD` header. `HEAD` is
  answered like `GET`, without the body.

Every answer carries `Cache-Control: no-store`, and every JSON body
`Content-Type: application/json`.

## How it observes

1. EC2 `DescribeInstances` for the configured instance only.
2. If EC2 reports it `running`, Systems Manager `DescribeInstanceInformation`
   for that instance and an HTTPS `GET` of the join page, at the same time and
   independently of each other.

The three are taken during one request, not as an atomic snapshot. The whole
observation is bounded at 6 seconds, because the site's client gives up at 7
and its router at 8; each provider call and the probe keep their own 3-second
limit. The Lambda timeout is 10 seconds. A call cut off by the bound reports
`unknown` (or `unreachable` for the probe), never a guess.

The probe verifies TLS, follows no redirects and reads at most 64 KiB of the
body, which it discards. Only HTTP 200 within those limits is `responding`; a
redirect (such as to the license page), another status or a larger body is
`unexpected_response`.

## Logs

The controller writes JSON lines to standard error, which Lambda sends to the
function's log group. Each `GET /server` writes a `status` line with the
reported state, each check and its reason, the join page's HTTP status and the
agent's last ping. Reasons are fixed categories: `observed`, `no_record`,
`unexpected_record`, `unexpected_state`, `access_denied`, `timeout`,
`canceled`, `provider_error`, `unexpected_status`, `response_too_large`,
`tls_error`, `network_error`, `response_error`, `host_not_running` and
`host_unknown`. The line is a warning when EC2 could not be read. Every
request also writes a `request` line with the method, the route pattern (never
the path), the status code and the time taken. No line carries a header,
a body or the Member.

## Configuration

All three settings are required:

| Variable | Deployed value |
| --- | --- |
| `FOUNDRY_AWS_REGION` | `us-west-2` |
| `FOUNDRY_INSTANCE_ID` | The host's instance ID, from `aws_instance.host`, so a rebuild updates it |
| `FOUNDRY_HTTPS_URL` | `https://foundry.craigdevjohnson.com:30000/join` |

The URL must be exactly `https://host[:port]/join`, with no credentials, query
or fragment. A missing or invalid setting stops the controller before it
serves: it logs `invalid_configuration` with the setting's name and exits with
status 1. On Lambda that is an init error, which the function's
`foundry-<env>-controller-failing` alarm counts. AWS credentials come from the
SDK's standard chain: the function's role on Lambda, your own locally.

The runtime needs only `ec2:DescribeInstances`,
`ssm:DescribeInstanceInformation` and its own log group. It sends no host
commands and starts nothing.

## Running it locally

`task run` serves the same interface at `http://127.0.0.1:8083` with the
credentials in your environment. It reads AWS and changes nothing:

```sh
AWS_PROFILE=workloads-readonly task run
curl -s http://127.0.0.1:8083/server
```

The three settings default to `us-west-2`, the host found by its
`Name=foundry` and `project=foundry` tags, and the join page; set any of them
to override it. `FOUNDRY_ADDR` changes the address. The Lobby's site can't
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
