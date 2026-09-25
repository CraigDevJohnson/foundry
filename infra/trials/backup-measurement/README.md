# Foundry backup measurement trial

<!-- markdownlint-disable MD013 -->

Status: offline template; no AWS deployment or mutation is approved here.
`foundry` owns the disposable trial resources and isolated state. `aws-setup`
owns the shared scheduled backup vaults.

## Scope

This root declares two temporary AWS Backup vaults, one in Oregon and one in
Ohio. Both use their regional `aws/ebs` KMS key ARNs. Their minimum and maximum
retention are seven days. Omitting `changeable_for_days` selects governance-mode
Vault Lock. `force_destroy = false` prevents Terraform from deleting nonempty
vaults.

The root also creates a Foundry-owned role named with `AWSBackup`, a custom
backup/copy policy, and an identical maximum-permissions boundary. It grants no
operator `iam:PassRole` and creates no backup selection, plan, recovery point,
volume, or instance. It never adopts the existing host or snapshots.

The measurement allows at most three Oregon source recovery points, three Ohio
copies, and 64 changed-block reads. It includes no restore.

## Before approval

From the source repository root, run `python3 scripts/check_feature_review.py`
and the `tests/test_feature_review.py` tests. The owner-local Stage A record at
`docs/reviews/issue-13-stage-a.json` cites the adopted foundation baseline.
CI and every live use of this root must pass that completeness gate. It checks
the review record, not live controls or Craig's execution approval. The record
currently covers the boundary bootstrap; update the owner-local review for
the later full trial apply before that path is executed.

The execution plan must record:

- The exact account, local state path, source instance and attached volume ARNs,
  approved trial resource IDs, seven-day job lifecycles, changed-block read and
  transfer limits, rollback and cleanup, and owner.
- Evidence that both supplied key ARNs resolve to `alias/aws/ebs` in their
  respective Regions.
- Evidence that no competing AWS Backup jobs will use this passed role or the
  trial vaults. Schedule capture outside live play or GM preparation and account
  for crash-consistent capture and I/O impact.

The custom role policy derives from the EC2/EBS, vault, and KMS portions of
[AWS Backup's v30 backup service policy][aws-backup-v30] and
[AWS's EC2 customer-managed policy recipe][aws-ec2-recipe]. It has no restore
policy or unrelated service permissions. Validate and simulate effective
allowed and denied access to source instances, copies, keys, tags, and cleanup
before AWS approval.

AWS says the role initiating a cross-Region copy needs `kms:DescribeKey` on
`alias/aws/backup`. The Oregon alias existed in the 2026-09-24 read-only
inventory. The passed role and its boundary grant only this read action on its
verified Oregon key ARN; they grant no cryptographic use of that key. The
operator policy needs the matching read grant. The Ohio EC2 vault still uses
`aws/ebs`. The first approved tagged copy is the proof gate; stop on a key or
permission mismatch rather than widening access in place.

A separately reviewed operator policy must limit `StartBackupJob` to the exact
Oregon trial vault, `StartCopyJob` to trial-tagged points, and `iam:PassRole` to
this role for AWS Backup. IAM authorizes `StartBackupJob` on a vault. The passed
role's `CreateImage` permission and the reviewed job command constrain the
source instance; the role also permits snapshots of the two named source
volumes. The operator must not widen or remove the role boundary. The role and
live jobs require Craig's concrete plan approval and work authorization.

## One-use boundary bootstrap

The trial operator must not create its own permissions boundary. Craig selected
an isolated MFA-protected root `aws login` bootstrap. After the separate
`aws-setup` foundation plan, a second saved plan in the same approved root
session creates only `aws_iam_policy.backup_boundary` from this Foundry root.
Use the Foundry-approved private local state path that the later trial apply
uses. Save a
targeted Terraform plan for that one resource, review its exact policy JSON,
name, tags, account, and sole create action, and apply only the saved plan.
Stop if Terraform proposes a role, vault, lock, or any other action.

`-target` is exceptional here because the boundary must exist before the
delegated role can be created. After the boundary apply, record its ARN and
default policy version, make a protected state checkpoint, and run a full
read-only plan. The later scoped trial operator may apply only the six
remaining resources through a new saved plan. The boundary stays in Foundry
state and remains attached until the role and every recovery point are gone.
The root session must not use the existing long-term root key. The
`aws-setup` bootstrap plan defines the isolated login profile and a
`root-bootstrap-process` adapter for this provider; use that adapter for
both saved Terraform plan and apply, after the guard verifies the exact root
identity. Never print the process credential output.
The two roots have separate source revisions and protected state paths. Stop
for a new authorization if the work window expires before this boundary plan.

## Job sequence and policy limits

The static role policy permits `backup:CopyFromBackupVault` only for Oregon EC2
AMI recovery points tagged `TrialId=i13-20260924`. It permits
`backup:CopyIntoBackupVault` only for the exact Ohio trial vault. No IAM policy
update follows capture.

The approved sequence is one Oregon backup at T0, T0+12 hours, and T0+24 hours.
Pass `RecoveryPointTags` with the exact `TrialId` on each backup job and verify
the resulting AMI tag. Copy completed points sequentially to Ohio before their
seven-day expiry. The first Ohio copy must reach `COMPLETED` with a queryable
recovery point before the T0+12-hour capture; this proves the copy path, not
restoration. If timing makes this impossible, stop as inconclusive.

| Slot | Capture | Backup idempotency token | Copy idempotency token |
| --- | --- | --- | --- |
| 0 | T0 | `i13-20260924-b0` | `i13-20260924-c0` |
| 1 | T0+12 hours | `i13-20260924-b1` | `i13-20260924-c1` |
| 2 | T0+24 hours | `i13-20260924-b2` | `i13-20260924-c2` |

Record each exact request and job ID in private evidence. If a start response
is uncertain, list and reconcile jobs and recovery points before retrying
with the same token and parameters. Stop if a job's identity or the point count
cannot be established. Never use a fresh token to retry an uncertain request.
Both [StartBackupJob][start-backup-job] and [StartCopyJob][start-copy-job]
support idempotency tokens.

AMI and snapshot IDs are unknown when the policy is written. EC2 copy and
tagging grants therefore use regional resource patterns. The caller's exact
job scope and read-back matter. Nested EBS snapshots inherit source-volume
tags and may lack `TrialId`. The passed role has region-scoped
`ec2:DeleteSnapshot` on snapshot ARNs without a tag condition, matching AWS
Backup's lifecycle policy. The operator gets no direct snapshot-delete grant.

Snapshot-attribute changes require the AWS Backup source-resource tag. AMI
deregistration and attribute changes require `TrialId`. Review the residual
snapshot-delete scope and actual tag behavior before approval. A missing AMI
tag or denied lifecycle action stops the trial and triggers exact, separately
reviewed cleanup, never a broader grant.

## Verification status

A protected read-only `terraform plan -refresh=false` and `show -json` after
source review returned seven creates. IAM Access Analyzer validation returned no
findings for the role identity, trust, and boundary policies. These checks do
not prove effective access or grant approval. No apply, deployment, or backup
job occurred.

Offline syntax checks need no AWS credentials:

```sh
terraform -chdir=infra/trials/backup-measurement fmt -check
terraform -chdir=infra/trials/backup-measurement init -backend=false
terraform -chdir=infra/trials/backup-measurement validate
```

## Local state and recovery

The local backend has an intentionally empty `path`. Supply an approved absolute
path outside this repository at initialization. Choose a private state directory
and a separate checkpoint directory on this FileVault volume, both outside Git.
Create each with
mode `0700`, set `umask 077` before every Terraform command, and verify that
each state file, Terraform-generated backup, and saved plan has mode `0600`.
For review, the proposed state path is
`/Users/craigjohnson/.local/share/foundry/issue-13-measurement/state`, with
checksummed copies under
`/Users/craigjohnson/.local/share/foundry/issue-13-measurement/checkpoints`.
Both would be on this Mac's FileVault-protected volume. Craig accepted this
same-Mac checkpoint design for the disposable trial. A second directory
protects against accidental overwrite but not disk loss; the exact recovery
fallback is the manual inventory/cleanup path below. This Foundry state must
not enter the `aws-setup` foundation bucket. These directories have not been
created; their exact paths remain inputs to the approved execution plan.
After choosing and reviewing both absolute paths:

```sh
umask 077
install -d -m 0700 "$TRIAL_STATE_DIR" "$TRIAL_BACKUP_DIR"
terraform -chdir=infra/trials/backup-measurement init \
  -backend-config="path=$TRIAL_STATE_DIR/terraform.tfstate"
```

Never initialize with an empty, relative, or in-repository `TRIAL_STATE_DIR`.
Keep the same path for the trial; changing it requires a reviewed state
migration. After the first state write, and before and after every later
state-writing operation, make an out-of-Git checkpoint. Keep the checkpoint
directory private and encrypted; if it shares the Mac's FileVault volume,
retain the documented disk-loss/import contingency:

```sh
TRIAL_BACKUP_ID="$(date -u +%Y%m%dT%H%M%SZ)"
TRIAL_CHECKPOINT_DIR="$TRIAL_BACKUP_DIR/$TRIAL_BACKUP_ID"
test ! -e "$TRIAL_CHECKPOINT_DIR"
install -d -m 0700 "$TRIAL_CHECKPOINT_DIR"
cp -p "$TRIAL_STATE_DIR/terraform.tfstate" \
  "$TRIAL_CHECKPOINT_DIR/terraform.tfstate"
if [ -f "$TRIAL_STATE_DIR/terraform.tfstate.backup" ]; then
  cp -p "$TRIAL_STATE_DIR/terraform.tfstate.backup" \
    "$TRIAL_CHECKPOINT_DIR/terraform.tfstate.backup"
fi
chmod 0600 "$TRIAL_STATE_DIR"/terraform.tfstate* \
  "$TRIAL_CHECKPOINT_DIR"/terraform.tfstate*
shasum -a 256 "$TRIAL_CHECKPOINT_DIR"/terraform.tfstate* \
  > "$TRIAL_CHECKPOINT_DIR/SHA256SUMS"
shasum -a 256 -c "$TRIAL_CHECKPOINT_DIR/SHA256SUMS"
```

If the primary state is lost, stop all plans and applies. Verify the chosen
checkpoint with `shasum -a 256 -c`, copy its exact `terraform.tfstate` bytes
back to the original approved path at mode `0600`, and reconcile
`terraform state list` with read-only AWS inventory before proceeding. Never
run a fresh apply against lost state.

If no valid state backup exists, use a separately approved manual cleanup:

1. In both Regions, enumerate vaults and tags. Match the exact account,
   `TrialId`, `Project=foundry`, `Purpose=backup-measurement`, and recorded vault
   names and ARNs. Check jobs, recovery points, AMIs, and snapshots.
2. Wait for seven-day points to expire. Remove only those governance locks and
   empty vaults, then read back their absence.
3. Keep the passed role and boundary until every trial recovery point, AMI,
   and snapshot is gone. Verify exact IAM names and tags, then remove its inline
   policy, role, and boundary in that order. Read back IAM absence.

Do not use force deletion or touch an untagged or mismatched resource. Preserve
the cleanup inventory and cost evidence even if state cannot be recovered.

The provider version is pinned. The included `.terraform.lock.hcl` has
macOS arm64 and Linux amd64 checksums; review it before execution. Keep state, saved
plans, and plan JSON out of Git. A later approved execution must save and
review the exact plan, pass the scoped service role to each backup/copy job,
measure billed bytes, changed-block reads, and copy time, and record job and
cleanup evidence. This README alone authorizes no apply, destroy, or further
live AWS plan.

For normal cleanup, stop new jobs and wait for each seven-day recovery point to
expire. Verify all trial AMIs and snapshots are gone and both vaults are empty.
Then review a saved destruction plan for only this trial. Terraform dependency
order deletes vaults before the passed role and boundary. Do not enable force
deletion, remove an unrelated lock, or delete the source host or its existing
snapshots. A failed copy or cleanup leaves the trial open for Craig's decision.

[aws-backup-v30]: https://docs.aws.amazon.com/aws-managed-policy/latest/reference/AWSBackupServiceRolePolicyForBackup.html
[aws-ec2-recipe]: https://docs.aws.amazon.com/aws-backup/latest/devguide/security-iam-awsmanpol.html#customer-managed-policies
[start-backup-job]: https://docs.aws.amazon.com/aws-backup/latest/APIReference/API_StartBackupJob.html
[start-copy-job]: https://docs.aws.amazon.com/aws-backup/latest/APIReference/API_StartCopyJob.html
