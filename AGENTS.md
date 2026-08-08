# AGENTS.md

## Architecture Philosophy: Stupid Simple, Actually Works

Core rule: build the most minimal code that solves the real problem. Not less, not more.

- **Simplest thing first.** Before writing code, ask "what's the most obvious way to make this work?" Do that. Don't reach for abstractions, frameworks, or patterns until the simple version actually fails to scale or breaks.
- **No over-building.** No speculative extensibility, no config knobs nobody asked for, no interfaces for a single implementation, no premature abstraction layers.
- **No over-testing.** Test the behavior that matters. Don't chase 100% coverage or write tests for edge cases that can't realistically occur. A few sharp tests > a pile of defensive ones.
- **No over-analyzing edge cases.** Handle the cases that will actually happen. Don't design for hypothetical inputs or failure modes that add complexity without real-world payoff.
- **Elegance over cleverness.** Prefer boring, readable code over clever tricks. If a reviewer needs to think hard to understand it, simplify it.
- **No AI slop.** No filler comments, no restating-the-obvious docstrings, no defensive try/catch wrapping everything, no unnecessary helper functions that just rename a one-liner, no dead code paths "for future use." Every line should earn its place.
- **Centralize, don't duplicate.** Shared logic lives in one maintainable place (a module, service, or utility), not copy-pasted across call sites. When adding a new feature, check if existing code already does 80% of the job — extend it instead of writing a parallel path.
- **Use what the platform already gives you.** If the deployment target has solid, widely-supported tooling that does the job, use it instead of reinventing it in application code.
  - Need fast text search? Use `ripgrep`, don't write a custom scanner.
  - Need remote filesystem access? Use `sshfs`, don't write custom protocol/transport code.
  - Need a job scheduler, process supervisor, reverse proxy, etc.? Use the standard OS/ecosystem tool, not a hand-rolled version.
  - Only write custom code when no reasonably-available tool does the job, or the tool genuinely doesn't fit.
- **Minimal code footprint wins.** When two approaches both work, prefer the one with less code, fewer moving parts, and fewer dependencies to maintain — even if the fancier one feels more "proper."

When in doubt: write the dumbest version that correctly solves the actual problem in front of you, not the problem you imagine might exist later.


## UX Contracts

`docs/ux-contracts.md` is the canonical, ground-truth inventory of every user-facing
feature, trigger, edge case, and keyboard shortcut in the app. It has a Table of
Contents at the top for fast routing to the relevant section.

- **Always read the relevant section(s) before implementing anything user-facing** —
  additions, removals, renames, or restructuring. It exists specifically to stop
  refactors from silently deleting or morphing features nobody remembers deciding on.
- **Always update the doc after implementing**, in the same change, not later: add new
  entries for new behavior, correct or delete entries for removed/changed behavior.
- If code and the doc disagree, code is ground truth — fix the doc as part of the change.
- Read the file in full before editing it; a partial/truncated read-modify-write has
  corrupted this file before. If your read tool truncates on a large file, read it in
  chunks (offset/limit) or edit via a targeted, verified string replacement instead of a
  full rewrite.

## Releasing

`pkg/common/version.go` (`VERSION`) is the single source of truth for the app version;
`web/package.json`/`package-lock.json` must stay in sync with it.

1. Run `./scripts/release.sh [patch|minor|major]` — bumps and verifies all three files
   consistently. It does NOT commit, tag, or push.
2. Update `CHANGELOG.md` with a new `## [X.Y.Z]` entry.
3. Commit, then `git tag -a vX.Y.Z -m "Release vX.Y.Z"` and `git push origin master vX.Y.Z`.
   Pushing the tag triggers the GoReleaser workflow, which publishes the release.
4. Never hand-edit only `pkg/common/version.go` — the CI tag-vs-version check and the
   frontend package files will drift out of sync.
