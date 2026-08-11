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
3. **Turn-level transcript scan (Claude).** On the Stop hook, `notify` already receives `transcript_path`. Extend it to scan the turn's JSONL for `tool_use` entries of write-ish tools (including nested subagent/Task transcripts when referenced) and send the union of paths as a final PostToolUse-style event. Catches subagent edits and hook misses once per turn.
4. **Pi fabric coverage.** Extend the Pi extension (`pkg/commands/agent-setup/pi-extension/termyard.ts`) to detect file writes performed inside `fabric_exec` programs. Mechanism to confirm during implementation: if Pi emits per-core-tool events inside fabric programs, subscribe to those; otherwise parse the fabric_exec tool result/args for `pi.edit`/`pi.write` path arguments. Best effort; documented if impossible.
5. **Deletion cleanup.** `/api/artifacts` refresh removes entries whose stat fails (rather than badging). A just-written file that disappears is dropped on the next refresh; no `stale` flag in responses. UI stale-badge rendering removed.
6. **Session-scoped identity.** Artifacts cleared when a session is killed. Artifact store keyed by host + session name + session created-at (or cleared on session-created event for a matching name). Persisted artifacts expire after 7 days.

## Non-goals

- Parsing bash command strings for paths (read/write ambiguity produces false positives; revisit only if coverage is still lacking).
- Wiring the tier-2 PTY output scanner (`ScanArtifactPaths`) or the unused OSC 7/8 parsers.
- mtime-window verification of writes (trusted env; tool-name allowlist is sufficient).
- Filesystem watching, git integration.
- Any change to tool-event lifecycle side effects (`Record`'s events/lastActive/activePanes, alerts, active-turn reconciliation, session cards).

## Risks

- Removing the cwd boundary exposes arbitrary host paths in the UI if a hook lies. Accepted: trusted environment.
- Transcript scan runs at Stop; very long transcripts add latency to the notify call. Mitigate: scan only the tail since the last user prompt.
- Pi fabric detection may be impossible without upstream Pi support; item 4 is best effort.
- Dropping the `stale` flag changes API shape; frontend and ux-contracts must move in the same change.

## Acceptance criteria

- A Claude subagent (Task) editing `/tmp/x.md` results in `/tmp/x.md` in the widget after the turn ends.
- A Read tool call on an existing file adds nothing to the widget.
- Deleting a listed file then pressing Refresh removes it from the list.
- Killing a session and creating a new one with the same name shows an empty widget.
- A Pi session in fabric full-code mode writing a file via `pi.write` shows that file (or the limitation is documented with the blocking fact).
- Absolute path outside session cwd (e.g. `/tmp/out.csv`) written by Edit appears in the widget.
- Existing tool-event UI (alert banner, session cards, active turns, /api/stats) behavior unchanged.
