---
status: accepted
---

# Protect completed sessions and use guided same-day recovery

For a personal game of at most five concurrent users, availability is required
during play rather than continuously. The operator explicitly starts and ends
sessions, with a backup before shutdown and a recoverable backup before
upgrades; recovery protects completed sessions, may lose in-progress session
changes, and must be achievable through a tested, guided restore within the
same day. This accepts a bounded interruption and data-loss window to keep
operating cost and complexity appropriate for the game, rather than requiring
continuous availability or recovery of every in-session write.

## Operating policy

Sessions include solo GM preparation. The operator confirms that nobody will be
interrupted before End session or a service restart. If the session backup
fails or its success cannot be confirmed, the host remains running and normal
shutdown is blocked until a successful retry.

Retain a successful session backup for each of the latest three distinct
completed sessions as a dedicated session-end series. A replacement may briefly
make four while its Ohio copy and protection are verified. After verification,
remove the oldest exact versions in both Regions. Keep scheduled recovery points
for 30 days in Oregon and Ohio. They may contain older session state but do not
count toward the three-session cap. Retain pre-upgrade backups outside the
three-session cap until the operator explicitly releases them. The
[session archive protection exception](0004-session-archive-protection.md)
records why this series uses different protection from scheduled backups.
Scheduled points count as usable history only when the application writer is
stopped or quiesced and complete reconstruction inputs are captured. Defer a
due capture during active play/preparation rather than interrupting it
automatically; record and alert on any freshness gap.

If replacement verification fails, retain the prior three and pending fourth,
then alert and retry. If unresolved within 24 hours of failure or before the
next session, whichever occurs first, return to Craig for a decision.

End session may stop the host after a complete, verified Oregon backup. Until
its Ohio copy is usable, report regional protection as pending and alert on a
copy failure. Check integrity for every backup and conduct an isolated restore
drill before first use, after relevant recovery or infrastructure changes, and
quarterly. Temporary recovery resources belong in the cost estimate; an
integrity check alone is not proof of restoration.
