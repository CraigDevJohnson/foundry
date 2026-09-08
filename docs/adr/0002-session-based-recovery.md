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

Retain the latest 10 successful session backups and retain pre-upgrade backups
until the operator explicitly releases them. Check integrity for every backup
and conduct an isolated restore drill before first use, after relevant recovery
or infrastructure changes, and quarterly. Temporary recovery resources belong
in the cost estimate; an integrity check alone is not proof of restoration.
