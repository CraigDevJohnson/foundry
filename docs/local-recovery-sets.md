# Local recovery-set capture and isolated materialization

This is the offline filesystem portion of [Issue #6](https://github.com/CraigDevJohnson/foundry/issues/6). It is tested with synthetic files and a simulated writer. It does not install host tooling, invoke AWS, stop EC2, upload a backup, manage retention or legal holds, or prove that an application can run in Ohio. The deployed host, its pins and its storage have not been inspected or changed by these tests.

## Inputs and coverage

`host/recovery.py` uses the existing `Host` methods and lifecycle lock. Its `capture` command is an explicit interruption, not a scheduled task. An authorized operator must confirm that neither play nor solo preparation is active. It validates the inventory and archive pins before stopping anything, then holds `foundry-lifecycle.lock` through host-input checks, verified service stop, mount/stopped rechecks, complete capture and any requested restart. It does not invoke the locking lifecycle CLI recursively. Current certificate renewal also takes that lifecycle lock, so it cannot change certificate files during capture. A disconnected caller or process using other host-management paths still requires the durable admission/reconciliation work in Issue #7.

Make a private, root-owned inventory from [the example](../host/recovery.inventory.example.json). The null placeholders deliberately prevent capture. `external_assets` must list every audited external filesystem asset root, or explicitly be empty after the coverage audit. This tool cannot infer remote assets, external databases, licensed-package availability or missing inputs from world configuration.

The inventory supplies:

- Actual retained Node and licensed Foundry distribution archives. Their content hashes must match the complete host manifest.
- Retained prerequisite RPM archives for every manifest NEVRA, with independently approved SHA-256 pins. Capture checks both bytes and RPM metadata without installing packages. Signature trust, transitive dependencies and compatibility with the retained Ohio image must be verified before live installation.
- A clean reviewed setup checkout whose HEAD equals `setup_revision`. Capture creates and verifies a standalone Git bundle, retaining the source and ancestry needed to recreate a checkout with Git metadata. No Git network access is used.
- The declared retained Ohio AMI ID. This is a required reconstruction reference, **not live availability or successful cross-Region image-copy proof**. Independently verify that image, its snapshots, access and keys before a regional restore.
- Explicit external asset roots. Overlapping roots and roots reached through symlinks are rejected.

Fixed coverage includes the entire `/foundry/foundrydata` tree, both manifest-declared worlds, Data, Config and existing Backups; the complete installed Foundry distribution; `/etc/foundry`; `/etc/letsencrypt`; installed host library/wrapper; and the service and renewal units. Logs, cache and temporary files within those trees are included with no filters. This conservative choice avoids silently discarding data and increases measured set size. Earlier backups and snapshots are never pruned. A future exclusion needs an explicit coverage decision.

Every captured file has SHA-256, length, original UID/GID, mode and modification time recorded. Empty directories are retained. Regular-file hard links are captured independently; special files are refused. In-tree certificate links and links to declared external assets are resolved to captured objects and remapped to relative links in the isolated tree. Links outside declared coverage, chains in a supplied set, recursive links, and path traversal are refused. Extended attributes, ACLs, sparse-file layout and hard-link identity are not replayed; the adopted host contract uses explicit ownership/modes. If a live coverage audit finds an application dependency on such metadata, extend the contract before capture is relied upon.

## Local set lifecycle

Capture requires a new output directory whose existing parent is owned by the operator, is not writable by group/others, and is on a different filesystem from the live data filesystem. That is a local failure-domain check, not a claim of off-host or regional protection. The storage must also meet the separately approved confidentiality and encryption policy. The host Config and certificate material are private: do not attach sets or their manifest contents to GitHub, chat, logs or test fixtures.

A set contains `manifest.json`, content-addressed regular files under `blobs/`, and a final `COMPLETE` marker. Creation uses a private directory and private files. The source-capture timestamp is taken immediately after verified stop; completion time is separate. Source fingerprints and stopped/mount evidence are checked again before completion. Data and the manifest are verified and flushed before the marker is written. Capture errors leave private partial evidence; no marker means the set is not a recovery point. Use a new output name for a retry. Never overwrite, automatically delete or promote partial evidence.

Capture prints only a status and manifest checksum. Preserve that checksum independently in the eventual trusted backup receipt. `verify` and `materialize` require it; reading a checksum from an untrusted set does not authenticate that set. Checksums detect corruption and substitution against an expected receipt; they are not a signature or a substitute for object-version/hold evidence.

After separately authorized installation/adoption, the explicit capture invocation is:

```sh
sudo python3 /reviewed/foundry/host/recovery.py capture \
  --inventory /private/recovery-inventory.json \
  --output /independent-private-storage/new-set \
  --confirm-no-active-session
```

Capture leaves the Foundry service stopped unless `--restart` is selected. It never stops EC2. If capture fails, do not assume the service is still serving and do not stop EC2. Reconcile the host. If only a requested restart fails after capture has completed, the valid set remains available; service recovery is a separate operation. The tool does not automatically restart after a failed/uncertain capture.

## Verify and materialize offline

These commands read a captured set and write only the explicitly selected new staging directory. They do not need a live host, root access, cloud credentials or a running Foundry installation:

```sh
python3 host/recovery.py verify --set /private/captured-set \
  --expected-sha256 FULL_CHECKSUM_FROM_TRUSTED_RECEIPT
python3 host/recovery.py materialize --set /private/captured-set \
  --expected-sha256 FULL_CHECKSUM_FROM_TRUSTED_RECEIPT \
  --output /private/new-isolated-tree
```

Materialization rejects existing destinations, symlinked or shared-writable destination parents, paths within the set and overlaps with its captured source roots. It validates the whole set before creating output, copies verified regular files before creating safe relative links, and checks content again while writing. It never writes to the original absolute paths from the manifest. All files remain private (`0600`) and directories private (`0700`); original ownership, modes and times are recorded but **not applied**. No service, mount, RPM, permission or certificate is installed or activated. Only a completed materialization receives `RECOVERY-MATERIALIZED.json`; that receipt explicitly says installation and Ohio restoration are unverified.

The retained source can be recreated without a network:

```sh
git clone /private/new-isolated-tree/setup_bundle /private/new-setup-checkout
```

A real restore must then independently verify the AMI and remaining reconstruction inputs, prepare an isolated host with DNS/schedules/outbound notifications disabled, apply the recorded reviewed paths/ownership/modes, verify both worlds/assets in the licensed application, and measure recovery time. These local commands do not replace those steps.

## Boundaries and remaining work

Recovery is now Data Lifecycle Manager snapshots of both volumes (aws-design §6.2 and §9.3), so the cloud upload, legal-hold rotation and regional copies these tools were built for won't be built, and the tools are not used on the workloads host. The restore procedure is in [operations](operations.md#restore-from-a-snapshot). Issue #6 closed with these offline tools, and issues #9 and #10 closed as not planned, superseded by the aws-setup refactor. End session is the controller's clean stop and takes no backup of its own (issue #7).

Run `python3 -m unittest discover -s tests -v`. The existing host workflow discovers these tests automatically. Fixtures exercise both-world round trips, source/archive pins, Git reconstruction, lifecycle locking, stop/mount/restart uncertainty, partial writes, corruption, changed inputs, special files, unsafe links, path traversal, destination isolation and checksum binding. No test accesses a real host or private application data.
