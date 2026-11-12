# Changelog

The notable changes to foundry, newest first. The repository has no tagged
releases, so changes are grouped by the month they merged to `main`, and each
entry names its pull request. Routine Dependabot updates are left out.

## November 2026

### Added

- Dependabot keeps the OpenTofu AWS provider up to date, and its pull requests
  carry the `dependencies` label (#36).
- Dependabot's minor and patch updates merge on their own once the checks
  pass (#40). GitHub Actions updates wait for a review (#42).
- Issue forms, a pull request template, `CONTRIBUTING.md`, a code of conduct
  and the MIT license (#35).
- This changelog.

### Changed

- The deploy role can read the state bucket's tags through S3 Control (#41).

### Security

- The controller builds with Go 1.27.4 for the `net/http` fix in
  GO-2026-6859 (#43).

## October 2026

### Added

- Foundry is served at https://foundry.craigdevjohnson.com through nginx on
  the host (#32, #33).
- The Lobby can start and stop the host, and changes to `infra` deploy from
  GitHub Actions (#23).
- The controller serves the host's status to The Lobby through a Function
  URL (#22).
- The host can be started and stopped from Task and from GitHub Actions (#21).
- A live-checks script, and the record of the 2026-10-27 live checks (#28,
  #29).

### Changed

- End session is the clean stop, and a missed certificate renewal is caught
  up (#26).
- A Taskfile replaces the Makefile (#20).

## September 2026

### Added

- The first OpenTofu root: snapshots through Data Lifecycle Manager and a
  least-privilege host role (#16).
- The host in the workloads account, with its transfer path and snapshots
  (#17).
- Operations docs (#18).
- Offline Foundry recovery sets (#15).
- A reproducible host setup and a guarded service lifecycle (#12).
- The read-only status controller (#11).
- The purpose and operating policy of the management plane (#3).

### Changed

- Session archives follow the three-copy recovery policy (#13).

### Removed

- The copies of the host in the management account (#18).
