# Status API v1

This is the read-only interface implemented by [issue #4](https://github.com/CraigDevJohnson/foundry/issues/4).
It describes three independent observations: EC2 lifecycle, Systems Manager
connectivity, and the configured Foundry HTTPS page. None alone proves that a
world is ready for play or that the application is supervised correctly.

## Request and response

The sole action is `get_status`. Send one JSON object with exactly these fields:

```json
{
  "version": 1,
  "action": "get_status",
  "request_id": "82bf536e-6157-48a7-95f9-801456f64c43"
}
```

Use a non-nil, hyphenated UUID request ID with version 1–8 and the RFC variant.
Duplicate or unknown keys, invalid values, trailing JSON,
and requests over 4 KiB are rejected before provider calls. The request cannot
choose an instance, region, URL, AWS role, or shell command.

A response can contain both known and unknown observations:

```json
{
  "version": 1,
  "kind": "status",
  "request_id": "82bf536e-6157-48a7-95f9-801456f64c43",
  "observed_at": "2026-09-08T18:00:00Z",
  "host": {"state": "running", "reason": "observed"},
  "ssm": {"state": "offline", "reason": "observed"},
  "https": {"state": "responding", "reason": "observed", "http_status": 200}
}
```

| Observation | States | Meaning |
| --- | --- | --- |
| `host` | `pending`, `running`, `shutting-down`, `terminated`, `stopping`, `stopped`, `not_found`, `unknown` | EC2's reported lifecycle state, or an explicit missing/unknown result |
| `ssm` | `online`, `offline`, `unknown`, `not_checked` | AWS's agent connectivity report; `last_ping_at` is included when available |
| `https` | `responding`, `unexpected_response`, `unreachable`, `unknown`, `not_checked` | Whether the configured `/join` page answered with HTTP 200 within the time/body limits |

If EC2 reports a non-running host, dependent observations are `not_checked`
with `host_not_running`. If the host cannot be determined, they use
`host_unknown`. For a running host, SSM and HTTPS are checked independently.
Missing SSM records are `unknown` with `no_record`, not evidence of an offline
agent. These are observations taken during one request, not an atomic snapshot.

The observation window is at most eight seconds, shortened by any caller
deadline. Provider calls and the HTTPS probe each have a three-second limit.
The probe verifies TLS, refuses redirects, and accepts response bodies no larger
than 64 KiB; page content is discarded. Unexpected HTTP status or an oversized body
does not become `responding`.

Reasons are fixed categories such as `observed`, `no_record`,
`unexpected_record`, `unexpected_state`, `access_denied`, `timeout`, `canceled`,
`provider_error`, `unexpected_status`, `response_too_large`, `tls_error`,
`network_error`, and `response_error`. Raw provider errors and page content are
not part of the wire contract.

Invalid requests or configuration return `kind: "error"` with
`error: {"code": "..."}` and no observations. Codes are `invalid_request`,
`invalid_request_id`, `unsupported_version`, `unsupported_action`,
`invalid_configuration`, and `provider_configuration_error`. A validated request
ID is echoed when available. Infrastructure or agent lookup failures instead
remain explicit component observations in a `kind: "status"` result.

## Configuration and local invocation

All three server settings are required:

| Variable | Example |
| --- | --- |
| `FOUNDRY_AWS_REGION` | `us-west-2` |
| `FOUNDRY_INSTANCE_ID` | `i-0123456789abcdef0` |
| `FOUNDRY_HTTPS_URL` | `https://foundry.example.invalid:30000/join` |

The URL must use HTTPS, have the exact `/join` path, and contain no credentials,
query, or fragment. The example target is illustrative; configure the intended
instance and endpoint before running. AWS credentials come from the SDK's
standard credential chain, including the deployment's execution role.

With those variables and read credentials configured:

```sh
printf '%s\n' '{"version":1,"action":"get_status","request_id":"82bf536e-6157-48a7-95f9-801456f64c43"}' |
  go run ./cmd/status
```

The local runner emits one JSON response. A structured request/configuration
error exits with code 1; a status response, including partial unknown results,
exits with code 0. This path performs real read-only queries and an HTTPS probe;
unit tests use fake providers and local test servers without AWS credentials.

## Portfolio and deployment contract

The portfolio authenticates its operator and invokes the fixed Lambda alias
server-side using AWS IAM. The native invocation is the authentication boundary;
an email or caller ARN supplied in JSON is not authentication. No public Lambda
Function URL or additional login is part of this interface.

Use synchronous `RequestResponse` invocation. Handle transport failures and
Lambda `FunctionError` before decoding the response; then distinguish structured
errors from partial status observations. Do not convert unavailable observations
to stopped, healthy, or ready states.

The runtime needs only `ec2:DescribeInstances` and
`ssm:DescribeInstanceInformation` for these observations, plus its normal logging
permissions. It does not send host commands or start Automation workflows.
The deployment must give the handler's eight-second deadline room to expire
before Lambda itself times out. Resource provisioning and portfolio integration
remain separate implementation issues.

## Build and checks

Run `make ci` for formatting, vet, race-enabled tests, the Linux ARM64 Lambda
build, and vulnerability analysis of the resulting binary. `make build` writes
`dist/bootstrap` and `dist/bootstrap.zip`; generated artifacts are ignored by
Git. The zip contains the executable `bootstrap` at its root for a Go custom
runtime on `provided.al2023` with the ARM64 architecture.
