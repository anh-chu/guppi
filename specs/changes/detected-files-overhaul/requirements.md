# Requirements: Detected Files Overhaul

## R1. Out-of-cwd artifacts accepted

GIVEN a session with cwd `/home/u/proj`
WHEN a PostToolUse event reports tool `write` with path `/tmp/report.csv` and that file exists
THEN the artifact list for the session contains `/tmp/report.csv`.

GIVEN the same session
WHEN an event reports relative path `notes.md`
THEN it resolves against the session cwd as today.

## R2. Write-tools only

GIVEN a PostToolUse event with tool `read` (or `grep`, `glob`) and a valid existing `file_path`
WHEN the server ingests it
THEN no artifact is recorded.

GIVEN tool `edit`, `write`, `multiedit`, `str_replace`, `apply_patch`, or `notebook_edit`
THEN artifacts are recorded as today. The allowlist is defined once server-side.

## R3. Pi fabric coverage (best effort)

GIVEN a Pi session in fabric full-code mode
WHEN a program calls `pi.write({path})` or `pi.edit({path})`
THEN the termyard Pi extension reports those paths as a PostToolUse write event, if Pi exposes the necessary event or the paths are recoverable from the fabric_exec tool call. If Pi exposes no such event, the limitation is documented in ux-contracts and this requirement is waived with the blocking fact recorded. No speculative parsing of fabric program source.

## R4. Deleted files removed

GIVEN an artifact whose file has been deleted
WHEN the client calls GET /api/artifacts (Refresh or mount)
THEN the entry is absent from the response and evicted from the tracker store. The `stale` field is removed from the API and UI.

## R5. Session-scoped lifetime

GIVEN a session is killed via the UI or API
THEN its artifact entries are deleted from the tracker and from persistence.

GIVEN a session name is reused after a kill
THEN the new session starts with zero artifacts.

GIVEN persisted artifacts older than 7 days
WHEN the server loads state
THEN they are dropped.

## R6. No regression to tool-event surfaces

GIVEN the changes above
THEN alert banner, session card status/activity, /api/stats counts, and active-turn reconciliation behave exactly as before (tool-event lifecycle code paths untouched except artifact handling).
