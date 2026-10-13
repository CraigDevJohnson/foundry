---
status: accepted
---

# Keep the interface in the portfolio and Foundry operations here

> **Interface superseded on 2026-10-13** by [ADR 0005](0005-the-lobby-calls-the-controller-through-a-function-url.md):
> The Lobby, not the portfolio, is the management interface. This repository
> still owns the Foundry backend, operations and host setup.

The portfolio provides the operator interface, while this repository owns the
Foundry backend, operational workflows, and reproducible host and service
setup. This reuses the existing management interface and login boundary while
giving Foundry operations a dedicated home, instead of creating a separate
management app or limiting access to local command-line tools. The existing
portfolio login must be verified before integration; the backend invocation
and authorization contract remains to be designed.
