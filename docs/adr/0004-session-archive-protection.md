---
status: accepted
---

# Protect the three session archives with per-version legal holds

The latest three completed sessions must remain recoverable even when sessions
are infrequent. A fixed 30-day AWS Backup Vault Lock retention would keep more
than three session-end checkpoints if four sessions occurred in a month. Craig
accepted a scoped planning exception to the foundation's REC-02 protection
design on 2026-09-24. No AWS execution was authorized.

## REC-02 exception record

- **Control:** REC-02, protection of cross-Region Foundry recovery points.
- **Owner:** `foundry` owns dedicated session archives, legal holds, and
  rotation. `aws-setup` retains ownership of shared scheduled backup vaults.
- **Scope:** Only the dedicated three-session series uses held S3 object
  versions. The 30-day scheduled points remain in governance-locked AWS Backup
  vaults; unreleased pre-upgrade copies remain outside the three-session cap.
- **Reason:** Fixed 30-day retention cannot enforce exactly three session-end
  checkpoints when sessions occur more often than expected.
- **Residual risk:** A compromised principal with legal-hold and version-delete
  authority can remove a protected session archive. Unversioned deletes can
  conceal held versions behind delete markers.
- **Compensating controls:** Per-version legal holds verified in Oregon and
  Ohio, narrow rotation authority, per-Region version-ID records, delete-marker
  controls, integrity checks, alerts, a 24-hour/next-session escalation, and an
  isolated Ohio restore drill.
- **Approval:** Craig's
  [Issue #12 decision supplement](https://github.com/CraigDevJohnson/aws-setup/issues/12#issuecomment-5824745778)
  on 2026-09-24 approves the planning exception and failed-verification rule,
  not AWS execution.
- **Review trigger:** Revisit before changing the session cap, scheduled
  retention, Region coverage, hold or delete authority, or archive technology,
  and after any failed rollover or unauthorized marker or hold change.

## Decision

- Store the dedicated session-end series as complete archives in private,
  versioned, Object Lock-enabled S3 buckets in Oregon and Ohio, encrypted at
  rest and in transit. Apply legal holds to every required object version,
  including reconstruction metadata. Record each Region's exact version IDs.
  Verify legal-hold **ON** for the usable recovery set in each Region. The host
  may upload but cannot release holds or delete retained versions.
- Keep ordinary 30-day scheduled recovery points in governance-locked AWS
  Backup vaults in both Regions. Scheduled points may contain older session
  state, but are not part of the three-session series. Do not include the
  session archive bucket in the scheduled AWS Backup S3 selection or remove the
  vault lock as the routine rotation mechanism. Keep unreleased pre-upgrade
  copies outside the three-session cap.
- Count a scheduled recovery point as usable only after proving the Foundry
  writer was stopped or quiesced and all required volumes, Data/Config, and
  reconstruction inputs were captured consistently. Do not interrupt active
  play or preparation automatically for the schedule. If a capture becomes due
  during a session, defer it, record the coverage gap, and retry after End
  session; alert and return for a decision if the accepted freshness target is
  at risk. A successful AWS Backup job or copy of a running writer is not proof
  of usable application recovery. Measure freshness from the latest qualified,
  usable Ohio point by source-capture time, not from a scheduled job's status.
- Permit a temporary fourth session archive during replacement. Only after the
  new archive, its Ohio copy, their integrity, and hold **ON** on the exact
  versions in both Regions are verified may a narrowly authorized management
  role release the oldest holds and delete those exact old versions in both
  Regions. If the Ohio copy or legal-hold check fails, retain the three prior
  known-good sessions and the pending fourth; do not prune a known-good point.
  Mark the temporary cap breach and regional-protection status, record exact
  version IDs, hold states, failure and retry evidence, elapsed time, and extra
  storage/copy cost exposure. Alert and retry. If unresolved within 24 hours of
  failed verification or before the next session, whichever occurs first,
  return to Craig for a decision. Do not silently begin another replacement or
  claim normal three-session retention. A partial prune stops further
  deletion, alerts the operator, and requires reconciliation of per-Region
  versions and recovery status before any further rotation.
- Prevent ordinary unversioned deletes from concealing held archives with
  delete markers; detect and reconcile any marker. Restore and prune by
  recorded version ID rather than by the current key alone. Prove the
  restricted role's allowed and denied actions and a full isolated Ohio
  restoration before relying on this design.

Legal holds protect individual versions only and can be removed by a principal
with `s3:PutObjectLegalHold`. An unversioned delete can still create a delete
marker, and deleting a version in Oregon does not delete its Ohio replica.
This exception gives the routine pruning role privileged deletion authority
over the session series. A sufficiently privileged principal can also remove
a governance vault lock; neither design is absolute against compromised
administrative authority. The execution plan must itemize storage in both
Regions, temporary and held versions, cross-Region copy, requests, encryption,
monitoring, role and control costs (including a zero line for IAM where
applicable), and restore-drill cost before approval.

## Sources

- [AWS Backup Vault Lock](https://docs.aws.amazon.com/aws-backup/latest/devguide/vault-lock.html)
- [S3 Object Lock and legal holds](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)
- [S3 replication and version deletion](https://docs.aws.amazon.com/AmazonS3/latest/userguide/replication-what-is-isnot-replicated.html)
