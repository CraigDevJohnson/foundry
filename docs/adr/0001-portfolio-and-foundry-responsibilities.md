---
status: accepted
---

# Keep the interface in the portfolio and Foundry operations here

The portfolio provides the operator interface, while this repository owns the
Foundry backend, operational workflows, and reproducible host and service
setup. This reuses the existing management interface and login boundary while
giving Foundry operations a dedicated home, instead of creating a separate
management app or limiting access to local command-line tools. The existing
portfolio login must be verified before integration; the backend invocation
and authorization contract remains to be designed.
