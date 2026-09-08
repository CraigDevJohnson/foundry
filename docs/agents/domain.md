# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

## Layout

This is a **single-context** repository:

- `CONTEXT.md` at the repository root holds the domain glossary and context.
- `docs/adr/` holds architecture decision records.

## Before exploring, read these

Read root `CONTEXT.md` and any ADRs in `docs/adr/` that touch the area you are about to work in.

If these files do not exist, **proceed silently**. Do not flag their absence or suggest creating them upfront. The `/domain-modeling` skill creates them lazily when terms or decisions actually get resolved.

## Use the glossary's vocabulary

When your output names a domain concept (in an issue title, a refactor proposal, a hypothesis, or a test name), use the term defined in `CONTEXT.md`. Do not drift to synonyms the glossary explicitly avoids.

If a concept is missing, reconsider whether it belongs to the project or note a real gap for `/domain-modeling`.

## Flag ADR conflicts

If your output contradicts an existing ADR, surface the conflict explicitly and explain why the decision may need to be reopened.
