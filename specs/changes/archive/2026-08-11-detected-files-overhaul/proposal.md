# Detected Files Overhaul

## Problem

The "detected files" widget records claims from agent PostToolUse hooks and stores them forever. Five verified defects:

1. Coverage gaps: Pi fabric full-code mode edits (`pi.edit`/`pi.write` inside `fabric_exec` programs) are invisible; Claude subagent coverage depends on per-tool hooks only.
2. Exec-style edits (bash, fabric_exec) produce no artifacts.
3. Deleted files are never removed, only badged `stale` on refresh.
4. No read-vs-edit distinction: Read tool calls with a `file_path` produce artifacts identical to Edit.
5. Identity leakage: artifacts keyed by `host\x00session` name, persisted forever; a reused session name shows a previous project's files. No cleanup on session kill, no TTL.

Additionally, `ResolveArtifactPath` drops any path outside the session cwd. Decision: the deployment is a fully trusted environment; this boundary hides real work (writes to /tmp, sibling repos, $HOME) and is removed.

## Desired outcome

The widget shows files the agent actually wrote this session, anywhere on disk, disappearing when deleted, scoped strictly to the current session lifetime.

## Scope

1. **Trust the event, drop the cwd boundary.** `ResolveArtifactPath` keeps cwd only for resolving relative paths and `~`; absolute paths anywhere are accepted. Existence stat (regular file) remains the only filter.
2. **Write-tools only.** Artifacts are recorded only for write-ish tools (write, edit, multiedit, str_replace, apply_patch, notebook_edit). Read/grep/glob tool inputs no longer produce artifacts. Tool-name allowlist lives server-side in one place so all integrations benefit.
3. **Pi fabric coverage.** Extend the Pi extension (`pkg/commands/agent-setup/pi-extension/termyard.ts`) to detect file writes performed inside `fabric_exec` programs. One bounded investigation: if Pi emits usable events for core tool calls inside fabric programs, subscribe; otherwise waive and document in ux-contracts. No speculative parsing.
4. **Deletion cleanup.** `/api/artifacts` refresh drops entries whose stat fails. `stale` flag removed from API and UI.
5. **Session-scoped lifetime.** Clear a session's artifacts on session kill. Persisted artifacts older than 7 days dropped on load. No key-schema change.

## Non-goals

- Parsing bash command strings for paths (read/write ambiguity produces false positives; revisit only if coverage is still lacking).
- Transcript scanning at Stop: Claude Code fires PostToolUse for subagent tool calls too, so per-tool hooks already cover subagents; no turn-level redundancy layer.
- Wiring the tier-2 PTY output scanner (`ScanArtifactPaths`) or the unused OSC 7/8 parsers.
- mtime-window verification of writes (trusted env; tool-name allowlist is sufficient).
- Filesystem watching, git integration.
- Any change to tool-event lifecycle side effects (`Record`'s events/lastActive/activePanes, alerts, active-turn reconciliation, session cards).

## Risks

- Removing the cwd boundary exposes arbitrary host paths in the UI if a hook lies. Accepted: trusted environment.
- Pi fabric detection may be impossible without upstream Pi support; item 3 is best effort.
- If Claude subagent PostToolUse hooks turn out not to fire in some setup, subagent coverage regresses to nothing; verify once during implementation before relying on it.
- Dropping the `stale` flag changes API shape; frontend and ux-contracts must move in the same change.

## Acceptance criteria

- A Claude subagent (Task) editing `/tmp/x.md` results in `/tmp/x.md` in the widget (via its own PostToolUse hooks).
- A Read tool call on an existing file adds nothing to the widget.
- Deleting a listed file then pressing Refresh removes it from the list.
- Killing a session and creating a new one with the same name shows an empty widget.
- A Pi session in fabric full-code mode writing a file via `pi.write` shows that file (or the limitation is documented with the blocking fact).
- Absolute path outside session cwd (e.g. `/tmp/out.csv`) written by Edit appears in the widget.
- Existing tool-event UI (alert banner, session cards, active turns, /api/stats) behavior unchanged.
