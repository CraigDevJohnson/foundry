## Summary

<!-- What changes, and why, in one or two sentences. -->

## Related issues

<!-- Closes #123 -->

## Checks

- [ ] `task ci` passes (Go formatting, vet, race tests, the Lambda build and govulncheck).
- [ ] If `infra/` changed: `tofu fmt -check -recursive infra`, and `validate` and `test` in `infra` and `infra/bootstrap`, pass.
- [ ] If `host/`, `scripts/` or `tests/` changed: `python3 -m unittest discover -s tests -v` passes.
- [ ] Docs match the change (README, `docs/`, `CONTEXT.md`, or a new ADR).

## Testing

<!-- What you ran and what you saw. -->

## Deploy

<!-- Merging changes nothing in AWS. Does this need a Deploy run, or a by-hand apply from Craig, afterwards, and what should its plan show? Write "None" if not. -->
